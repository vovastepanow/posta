"""Offline shape, initialization and checkpoint tests; no real data used."""
import io
import unittest
from dataclasses import replace
from spec import Config, parameter_counts, memory_floor

try:
    import torch
    from model import PostaMulti
    from av_frame_cache import cached_frame_embeddings, backward_recomputed_frames
    HAVE_TORCH=True
except ImportError:
    HAVE_TORCH=False


class AccountingTests(unittest.TestCase):
    def test_default_budget(self):
        self.assertEqual(parameter_counts()['total'], 235179777)
        self.assertEqual(memory_floor()['adam_fp32_weights_grad_m_v_bytes'], 16*235179777)

    def test_invalid_dimensions_rejected(self):
        with self.assertRaises(ValueError):
            parameter_counts(replace(Config(), text_heads=7))


@unittest.skipUnless(HAVE_TORCH, 'PyTorch not installed')
class ModelTests(unittest.TestCase):
    def setUp(self):
        self.c=Config(vocab_size=128,text_width=32,text_layers=2,text_heads=4,text_ff=64,text_length=16,
                      image_width=32,image_layers=2,image_heads=4,image_ff=64,image_size=32,image_patch=8,
                      embed_dim=16,temporal_width=16,temporal_layers=2,temporal_heads=4,temporal_ff=32,
                      temporal_length=32,fusion_layers=1,fusion_slots=4,mel_bins=16,audio_patch_mels=8,audio_patch_frames=4)
        torch.manual_seed(42)
        self.model=PostaMulti(self.c,dropout=0)

    def test_full_size_parameter_count_without_allocating_weights(self):
        with torch.device('meta'):
            full = PostaMulti(Config())
        self.assertEqual(sum(p.numel() for p in full.parameters()), 235179777)

    def test_seed_reproduces_initial_weights(self):
        torch.manual_seed(42)
        other=PostaMulti(self.c,dropout=0)
        self.assertTrue(torch.equal(self.model.text_tokens.weight,other.text_tokens.weight))
        self.assertTrue(torch.equal(self.model.image_patches.weight,other.image_patches.weight))

    def test_parameter_count_and_independent_layers(self):
        self.assertEqual(sum(p.numel() for p in self.model.parameters()),parameter_counts(self.c)['total'])
        a=self.model.text_encoder.layers[0].linear1.weight
        b=self.model.text_encoder.layers[1].linear1.weight
        self.assertFalse(torch.equal(a,b))

    def test_modalities_and_padding(self):
        self.model.eval()
        ids=torch.randint(0,self.c.vocab_size,(2,8));mask=torch.ones_like(ids,dtype=torch.bool)
        ids2=ids.clone();ids2[:,6:]=torch.randint(0,self.c.vocab_size,(2,2));mask[:,6:]=False
        with torch.no_grad():
            text=self.model.encode_text(ids,mask)
            self.assertTrue(torch.allclose(text,self.model.encode_text(ids2,mask),atol=1e-6))
            image=self.model.encode_image(torch.randn(2,3,32,32))
            video=self.model.encode_video(torch.randn(2,2,3,32,32), timestamps=torch.tensor([[0.,1.],[0.,2.]]))
            audio=self.model.encode_audio(torch.randn(2,16,16))
            slots=torch.stack((text,image,video,audio),dim=1)
            slots_mask=torch.tensor([[True,True,False,False],[True,False,False,False]])
            fused=self.model.fuse_post(slots,slots_mask)
            changed=slots.clone();changed[~slots_mask]=99
            self.assertTrue(torch.allclose(fused,self.model.fuse_post(changed,slots_mask),atol=1e-6))
        for output in (text,image,video,audio,fused):
            self.assertEqual(tuple(output.shape),(2,16))
            self.assertTrue(torch.isfinite(output).all())
            self.assertTrue(torch.allclose(output.norm(dim=-1),torch.ones(2),atol=1e-5))
        self.assertEqual(tuple(self.model.similarity(text,image).shape),(2,2))
        positions=torch.zeros_like(ids,dtype=torch.bool);positions[:,2]=True
        self.assertEqual(tuple(self.model.masked_token_logits(ids,mask,positions).shape),(2,self.c.vocab_size))
        with self.assertRaisesRegex(ValueError, 'nondecreasing'):
            self.model.encode_video(torch.randn(1,2,3,32,32),timestamps=torch.tensor([[2.,1.]]))


    def test_streamed_video_embeddings_match_direct_path_and_gradients(self):
        self.model.eval()
        frames = torch.randn(2, 3, 3, 32, 32)
        mask = torch.tensor([[True, True, True], [True, True, False]])
        times = torch.tensor([[0., .1, .2], [0., .2, 0.]])
        direct = self.model.encode_video(frames, mask, times)
        direct_grad = torch.autograd.grad(direct[:, 0].sum(), self.model.image_patches.weight)[0]
        chunks = [self.model.encode_image(frames[:, i]) for i in range(3)]
        streamed = self.model.encode_video_embeddings(torch.stack(chunks, dim=1), mask, times)
        streamed_grad = torch.autograd.grad(streamed[:, 0].sum(), self.model.image_patches.weight)[0]
        self.assertTrue(torch.allclose(direct, streamed, atol=1e-6, rtol=1e-5))
        self.assertTrue(torch.allclose(direct_grad, streamed_grad, atol=1e-6, rtol=1e-5))
        with self.assertRaisesRegex(ValueError, 'frame_embeddings'):
            self.model.encode_video_embeddings(torch.randn(2, 3, 17))

    def test_recomputed_frame_gradients_match_full_video_backward(self):
        self.model.eval()
        frames = torch.randn(1, 3, 3, 32, 32)
        times = torch.tensor([[0., .1, .2]])
        self.model.encode_video(frames, timestamps=times)[:, 0].sum().backward()
        expected = {name: p.grad.clone() for name, p in self.model.named_parameters()
                    if p.grad is not None}
        self.model.zero_grad(set_to_none=True)
        cached = cached_frame_embeddings(self.model, frames)
        self.model.encode_video_embeddings(cached, timestamps=times)[:, 0].sum().backward()
        backward_recomputed_frames(self.model, frames, cached)
        actual = {name: p.grad for name, p in self.model.named_parameters() if p.grad is not None}
        self.assertEqual(expected.keys(), actual.keys())
        for name in expected:
            self.assertTrue(torch.allclose(expected[name], actual[name], atol=2e-6, rtol=1e-4), name)
        self.model.train()
        with self.assertRaisesRegex(ValueError, 'eval mode'):
            cached_frame_embeddings(self.model, frames)

    def test_centered_time_has_no_constant_offset_and_legacy_is_unchanged(self):
        self.model.eval()
        embeddings = torch.nn.functional.normalize(torch.randn(2, 3, self.c.embed_dim), dim=-1)
        zero = torch.zeros(2, 3)
        with torch.no_grad():
            untimed = self.model.encode_video_embeddings(embeddings)
            centered = self.model.encode_video_embeddings(
                embeddings, timestamps=zero, time_encoding='relative-centered-v1')
            legacy = self.model.encode_video_embeddings(embeddings, timestamps=zero)
            explicit = self.model.encode_video_embeddings(embeddings, timestamps=zero, time_encoding='legacy')
        self.assertTrue(torch.allclose(untimed, centered, atol=1e-6))
        self.assertTrue(torch.equal(legacy, explicit))
        self.assertFalse(torch.allclose(untimed, legacy, atol=1e-4))
        with self.assertRaisesRegex(ValueError, 'time encoding'):
            self.model.encode_video_embeddings(embeddings, time_encoding='unknown')

    def test_new_pooling_is_padding_invariant(self):
        c = replace(self.c, text_pooling='mean')
        model = PostaMulti(c, dropout=0)
        model.eval()
        ids = torch.tensor([[1, 4, 5, 2, 0, 0], [1, 6, 7, 2, 0, 0]])
        mask = ids.ne(0)
        changed = ids.clone()
        changed[:, 4:] = 99
        with torch.no_grad():
            a = model.encode_text(ids, mask)
            b = model.encode_text(changed, mask)
        self.assertTrue(torch.allclose(a, b, atol=1e-6))
        self.assertTrue(torch.allclose(a.norm(dim=-1), torch.ones(2), atol=1e-6))
        with self.assertRaisesRegex(ValueError, 'content token'):
            model.encode_text(torch.tensor([[1, 2, 0]]),
                              torch.tensor([[True, True, False]]))

    def test_residual_pooling_preserves_lexical_distinctions_under_hidden_collapse(self):
        config = replace(self.c, text_pooling='mean-residual')
        model = PostaMulti(config, dropout=0)
        model.eval()
        ids = torch.tensor([[1, 5, 7, 2, 0],
                            [1, 22, 31, 2, 0]])
        mask = ids.ne(0)
        # Simulate the measured v8/v9 failure: identical hidden states for
        # distinct text examples. The raw-token shortcut must remain varied.
        def collapsed_hidden(token_ids, attention_mask):
            return torch.ones((token_ids.shape[0], token_ids.shape[1],
                               config.text_width), dtype=torch.float32)
        model._text_hidden = collapsed_hidden
        with torch.no_grad():
            embeddings = model.encode_text(ids, mask)
            self.assertTrue(torch.isfinite(embeddings).all())
            self.assertGreater(float((embeddings[0] - embeddings[1]).norm()), .001)
            model.config = replace(config, text_pooling='mean')
            collapsed = model.encode_text(ids, mask)
            self.assertTrue(torch.allclose(collapsed[0], collapsed[1], atol=1e-6))
            model.config = config
            changed_padding = ids.clone()
            changed_padding[:, -1] = 39
            self.assertTrue(torch.allclose(
                embeddings, model.encode_text(changed_padding, mask), atol=1e-6))
        self.assertEqual(parameter_counts(config)['total'],
                         parameter_counts(replace(config, text_pooling='mean'))['total'])

    def test_text_checkpointing_preserves_gradients(self):
        c = replace(self.c, text_pooling='mean')
        torch.manual_seed(91)
        baseline = PostaMulti(c, dropout=0)
        checkpointed = PostaMulti(c, dropout=0)
        checkpointed.load_state_dict(baseline.state_dict())
        checkpointed.enable_gradient_checkpointing()
        baseline.train()
        checkpointed.train()
        ids = torch.tensor([[1, 9, 11, 2], [1, 7, 8, 2]])
        mask = torch.ones_like(ids, dtype=torch.bool)
        first = baseline.encode_text(ids, mask)
        second = checkpointed.encode_text(ids, mask)
        self.assertTrue(torch.allclose(first, second, atol=1e-6))
        (first[:, 0] - 0.5).square().mean().backward()
        (second[:, 0] - 0.5).square().mean().backward()
        self.assertTrue(torch.allclose(baseline.text_projection.weight.grad,
                                       checkpointed.text_projection.weight.grad,
                                       atol=2e-5, rtol=2e-4))
        self.assertTrue(torch.allclose(
            baseline.text_encoder.layers[0].linear1.weight.grad,
            checkpointed.text_encoder.layers[0].linear1.weight.grad,
            atol=2e-5, rtol=2e-4))

    def test_invalid_pooling_is_rejected(self):
        with self.assertRaises(ValueError):
            parameter_counts(replace(self.c, text_pooling='invalid'))
        with self.assertRaises(ValueError):
            PostaMulti(replace(self.c, text_pooling='invalid'))

    def test_training_step_and_checkpoint(self):
        optimizer=torch.optim.SGD(self.model.parameters(),lr=0.01)
        x=torch.randint(0,self.c.vocab_size,(2,8)); mask=torch.ones_like(x,dtype=torch.bool)
        before=self.model.text_projection.weight.detach().clone()
        embedding=self.model.encode_text(x,mask)
        loss=(embedding[:,0] - 0.5).square().mean()
        loss.backward();optimizer.step()
        self.assertFalse(torch.equal(before,self.model.text_projection.weight))
        payload=io.BytesIO();torch.save({'model':self.model.state_dict(),'optimizer':optimizer.state_dict()},payload)
        payload.seek(0);checkpoint=torch.load(payload,map_location='cpu',weights_only=True)
        torch.manual_seed(99);restored=PostaMulti(self.c,dropout=0);restored.load_state_dict(checkpoint['model'])
        self.model.eval();restored.eval()
        with torch.no_grad():
            self.assertTrue(torch.allclose(self.model.encode_text(x,mask),restored.encode_text(x,mask)))


if __name__=='__main__': unittest.main()
