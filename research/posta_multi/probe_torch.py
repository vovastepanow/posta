#!/usr/bin/env python3
"""Synthetic Posta Multi 235M GPU forward/backward check with a strict memory cap.

Does not read datasets, save weights, or train a useful model. This checks a
single short-text/image SGD step, not full sequence/video/Adam feasibility.
"""
import argparse
import json
import time
import torch
from torch.nn import functional as F
if __package__:
    from .model import PostaMulti
    from .spec import Config, parameter_counts
else:
    from model import PostaMulti
    from spec import Config, parameter_counts


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--max-fraction',type=float,default=0.45)
    parser.add_argument('--sequence-length',type=int,default=32)
    args=parser.parse_args()
    if not 1 <= args.sequence_length <= Config().text_length:
        parser.error('sequence-length must be 1..512')
    if not 0 < args.max_fraction <= 0.5:
        parser.error('max-fraction must be greater than 0 and at most 0.5')
    if not torch.cuda.is_available():
        raise RuntimeError('PyTorch CUDA is unavailable')
    torch.cuda.set_device(0)
    capability=torch.cuda.get_device_capability(0)
    supported=torch.cuda.get_arch_list()
    compatible = []
    for arch in supported:
        if arch.startswith('sm_') and arch[3:].isdigit():
            value=int(arch[3:]); major,minor=divmod(value,10)
            if major==capability[0] and minor<=capability[1]:
                compatible.append(arch)
    if not compatible:
        raise RuntimeError(f'wheel has no compatible architecture for {capability}; wheel={supported}')
    # Validate the wheel with an actual tiny GPU operation before allocating the model.
    value=(torch.ones(8,device='cuda')*2).sum().item()
    if value != 16:
        raise RuntimeError('basic PyTorch CUDA calculation failed')
    free,total=torch.cuda.mem_get_info()
    if free < 1700 * 1024**2:
        raise RuntimeError('less than 1700 MiB free on GPU; skip to protect desktop')
    torch.cuda.set_per_process_memory_fraction(args.max_fraction,0)
    torch.manual_seed(42);torch.cuda.manual_seed_all(42)
    old_dtype=torch.get_default_dtype();torch.set_default_dtype(torch.float16)
    try:
        with torch.device('cuda'):
            model=PostaMulti(Config(),dropout=0)
    finally:
        torch.set_default_dtype(old_dtype)
    count=sum(p.numel() for p in model.parameters())
    if count!=parameter_counts()['total']:
        raise RuntimeError(f'parameter accounting mismatch: {count}')
    model.train()
    ids=torch.randint(0,model.config.vocab_size,(1,args.sequence_length),device='cuda',dtype=torch.long)
    mask=torch.ones_like(ids,dtype=torch.bool)
    image=torch.randn((1,3,224,224),device='cuda',dtype=torch.float16)
    target=F.normalize(torch.randn((1,512),device='cuda',dtype=torch.float32),dim=-1)
    optimizer=torch.optim.SGD(model.parameters(),lr=0.02)
    before=model.text_projection.weight.detach().clone()
    torch.cuda.synchronize();start=time.perf_counter()
    text=model.encode_text(ids,mask)
    visual=model.encode_image(image)
    empty=torch.zeros_like(text)
    slots=torch.stack((text,visual,empty,empty),dim=1)
    slot_mask=torch.tensor([[True,True,False,False]],device='cuda')
    fused=model.fuse_post(slots,slot_mask)
    loss=((text.float()-target).square().mean()+(visual.float()-target).square().mean()
          +(fused.float()-target).square().mean())
    if not torch.isfinite(loss):raise RuntimeError('non-finite synthetic loss')
    loss.backward()
    grad=model.text_projection.weight.grad
    if grad is None or not torch.isfinite(grad).all() or not torch.count_nonzero(grad):
        raise RuntimeError('invalid or zero text gradient')
    image_grad=model.image_projection.weight.grad
    if image_grad is None or not torch.isfinite(image_grad).all() or not torch.count_nonzero(image_grad):
        raise RuntimeError('invalid or zero image gradient')
    fusion_grad=model.fusion_encoder.layers[0].linear1.weight.grad
    if fusion_grad is None or not torch.isfinite(fusion_grad).all() or not torch.count_nonzero(fusion_grad):
        raise RuntimeError('invalid or zero fusion gradient')
    optimizer.step();torch.cuda.synchronize();duration=time.perf_counter()-start
    if torch.equal(before,model.text_projection.weight.detach()):
        raise RuntimeError('synthetic optimizer step did not change the text projection')
    primary_peak = torch.cuda.max_memory_allocated()
    primary_reserved = torch.cuda.max_memory_reserved()
    del text, visual, slots, fused, loss, grad, image_grad, fusion_grad, before, image
    optimizer.zero_grad(set_to_none=True)
    torch.cuda.empty_cache()
    auxiliary = {}
    for kind in ('video', 'audio'):
        try:
            torch.cuda.reset_peak_memory_stats()
            torch.cuda.synchronize(); tick=time.perf_counter()
            if kind == 'video':
                sample=torch.randn((1,2,3,224,224),device='cuda',dtype=torch.float16)
                result=model.encode_video(sample,timestamps=torch.tensor([[0.0,1.0]],device='cuda'))
            else:
                sample=torch.randn((1,80,64),device='cuda',dtype=torch.float16)
                result=model.encode_audio(sample)
            branch_loss=(result.float()-target).square().mean()
            if not torch.isfinite(branch_loss):
                raise RuntimeError('non-finite branch loss')
            branch_loss.backward()
            probe_grad=(model.temporal_encoder.layers[0].linear1.weight.grad)
            if probe_grad is None or not torch.isfinite(probe_grad).all() or not torch.count_nonzero(probe_grad):
                raise RuntimeError('invalid or zero temporal gradient')
            input_grad=(model.image_projection.weight.grad if kind == 'video'
                        else model.audio_patch_projection.weight.grad)
            if input_grad is None or not torch.isfinite(input_grad).all() or not torch.count_nonzero(input_grad):
                raise RuntimeError('invalid or zero modality input gradient')
            optimizer.step();torch.cuda.synchronize()
            auxiliary[kind]={'status':'ok','seconds':round(time.perf_counter()-tick,3),
                             'peak_allocated_bytes':torch.cuda.max_memory_allocated()}
            del sample, result, branch_loss, probe_grad, input_grad
        except RuntimeError as exc:
            auxiliary[kind]={'status':'error','error':str(exc)[:300]}
        finally:
            optimizer.zero_grad(set_to_none=True)
            torch.cuda.empty_cache()
    print(json.dumps({'status':'ok','gpu':torch.cuda.get_device_name(0),
                      'compute_capability':list(capability),'torch':torch.__version__,
                      'torch_cuda':torch.version.cuda,'parameter_count':count,
                      'gpu_free_before_bytes':free,'gpu_total_bytes':total,
                      'allocator_limit_fraction':args.max_fraction,
                      'peak_allocated_bytes':primary_peak,
                      'peak_reserved_bytes':primary_reserved,
                      'seconds_for_one_synthetic_step':round(duration,3),
                      'input':f'one random {args.sequence_length}-token text and one random 224x224 image, fused',
                      'optimizer':'FP16 SGD, no momentum','auxiliary_branch_steps':auxiliary,
                      'limitations':['single synthetic step only; no corpus used',
                                     'not sustained training, large batches, long video/audio, Adam or production throughput',
                                     'FP16 numerical stability on real data is untested']},indent=2))


if __name__=='__main__':
    try:main()
    except (RuntimeError,ValueError) as e:
        print(json.dumps({'status':'error','error':str(e)},ensure_ascii=False));raise SystemExit(1)
