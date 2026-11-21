"""Provisional deterministic UTF-8 byte tokenizer, not the final 65k tokenizer."""
PAD, CLS, SEP, MASK = range(4)
BYTE_OFFSET = 4
VOCAB_SIZE = 260
VERSION = 'posta-utf8-byte-v1'


def encode(text, max_length=512):
    if not isinstance(text, str) or max_length < 2:
        raise ValueError('text must be a string and max_length at least 2')
    data = bytearray()
    for char in text:
        encoded = char.encode('utf-8')
        if len(data) + len(encoded) > max_length - 2:
            break
        data.extend(encoded)
    return [CLS, *(byte + BYTE_OFFSET for byte in data), SEP]


def decode(ids):
    if len(ids) < 2 or ids[0] != CLS or ids[-1] != SEP:
        raise ValueError('expected CLS and SEP')
    if any(not BYTE_OFFSET <= token < VOCAB_SIZE for token in ids[1:-1]):
        raise ValueError('invalid byte token')
    return bytes(token - BYTE_OFFSET for token in ids[1:-1]).decode('utf-8')
