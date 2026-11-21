#!/usr/bin/env python3
"""Run a tiny synthetic CUDA driver test without PyTorch or dataset access."""
import ctypes as c
import json
import struct
import sys

PTX = b'''
.version 6.4
.target sm_61
.address_size 64
.visible .entry pmr_test(.param .u64 data, .param .u32 count) {
 .reg .pred %p;
 .reg .b32 %r<6>;
 .reg .b64 %rd<5>;
 .reg .f32 %f<4>;
 ld.param.u64 %rd1, [data];
 ld.param.u32 %r1, [count];
 mov.u32 %r2, %ctaid.x;
 mov.u32 %r3, %ntid.x;
 mov.u32 %r4, %tid.x;
 mad.lo.s32 %r5, %r2, %r3, %r4;
 setp.ge.u32 %p, %r5, %r1;
 @%p bra DONE;
 mul.wide.u32 %rd2, %r5, 4;
 add.s64 %rd3, %rd1, %rd2;
 ld.global.f32 %f1, [%rd3];
 mov.f32 %f2, 0f40000000;
 mul.f32 %f3, %f1, %f2;
 mov.f32 %f2, 0f3f800000;
 add.f32 %f3, %f3, %f2;
 st.global.f32 [%rd3], %f3;
DONE:
 ret;
}
'''


def main():
    lib = c.CDLL('libcuda.so.1')
    def setup(name, args):
        f=getattr(lib,name); f.argtypes=args; f.restype=c.c_int; return f
    init=setup('cuInit',[c.c_uint]); device_get=setup('cuDeviceGet',[c.POINTER(c.c_int),c.c_int])
    attribute=setup('cuDeviceGetAttribute',[c.POINTER(c.c_int),c.c_int,c.c_int])
    ctx_create=setup('cuCtxCreate_v2',[c.POINTER(c.c_void_p),c.c_uint,c.c_int])
    ctx_destroy=setup('cuCtxDestroy_v2',[c.c_void_p])
    mem_info=setup('cuMemGetInfo_v2',[c.POINTER(c.c_size_t),c.POINTER(c.c_size_t)])
    mem_alloc=setup('cuMemAlloc_v2',[c.POINTER(c.c_uint64),c.c_size_t])
    memset=setup('cuMemsetD8_v2',[c.c_uint64,c.c_ubyte,c.c_size_t])
    copy=setup('cuMemcpyDtoH_v2',[c.c_void_p,c.c_uint64,c.c_size_t])
    mem_free=setup('cuMemFree_v2',[c.c_uint64])
    module_load=setup('cuModuleLoadData',[c.POINTER(c.c_void_p),c.c_void_p])
    module_func=setup('cuModuleGetFunction',[c.POINTER(c.c_void_p),c.c_void_p,c.c_char_p])
    module_unload=setup('cuModuleUnload',[c.c_void_p])
    launch=setup('cuLaunchKernel',[c.c_void_p,c.c_uint,c.c_uint,c.c_uint,c.c_uint,c.c_uint,c.c_uint,c.c_uint,c.c_void_p,c.POINTER(c.c_void_p),c.c_void_p])
    sync=setup('cuCtxSynchronize',[])
    error_name=setup('cuGetErrorName',[c.c_int,c.POINTER(c.c_char_p)])
    def check(code, op):
        if code:
            name=c.c_char_p(); error_name(code,c.byref(name))
            raise RuntimeError(f'{op}: {name.value.decode() if name.value else code}')
    check(init(0),'cuInit'); dev=c.c_int(); check(device_get(c.byref(dev),0),'cuDeviceGet')
    major=c.c_int(); minor=c.c_int()
    check(attribute(c.byref(major),75,dev),'compute capability major')
    check(attribute(c.byref(minor),76,dev),'compute capability minor')
    ctx=c.c_void_p(); check(ctx_create(c.byref(ctx),0,dev),'cuCtxCreate')
    ptr=c.c_uint64(); mod=c.c_void_p()
    try:
        free=c.c_size_t(); total=c.c_size_t(); check(mem_info(c.byref(free),c.byref(total)),'cuMemGetInfo')
        count=c.c_uint(1024*1024); size=count.value*4
        check(mem_alloc(c.byref(ptr),size),'cuMemAlloc'); check(memset(ptr,0,size),'cuMemset')
        ptx_buffer=c.create_string_buffer(PTX)
        check(module_load(c.byref(mod),c.cast(ptx_buffer,c.c_void_p)),'cuModuleLoadData')
        fun=c.c_void_p(); check(module_func(c.byref(fun),mod,b'pmr_test'),'cuModuleGetFunction')
        args=(c.c_void_p*2)(c.cast(c.byref(ptr),c.c_void_p),c.cast(c.byref(count),c.c_void_p))
        check(launch(fun,(count.value+255)//256,1,1,256,1,1,0,None,args,None),'cuLaunchKernel')
        check(sync(),'cuCtxSynchronize')
        first=c.create_string_buffer(4); last=c.create_string_buffer(4)
        check(copy(first,ptr,4),'cuMemcpyDtoH first');check(copy(last,ptr.value+size-4,4),'cuMemcpyDtoH last')
        a,b=struct.unpack('f',first.raw)[0],struct.unpack('f',last.raw)[0]
        if (a,b)!=(1.0,1.0): raise ValueError('CUDA kernel produced unexpected values')
        print(json.dumps({'status':'ok','compute_capability':f'{major.value}.{minor.value}',
                          'free_bytes_after_context':free.value,'total_bytes':total.value,
                          'synthetic_buffer_bytes':size,'kernel_first':a,'kernel_last':b,
                          'limitation':'This is a CUDA driver/kernel smoke check, not a full model training or throughput test.'},indent=2))
    finally:
        if mod.value: module_unload(mod)
        if ptr.value: mem_free(ptr)
        ctx_destroy(ctx)


if __name__=='__main__':
    try: main()
    except (OSError, RuntimeError, ValueError) as exc:
        print(json.dumps({'status':'error','error':str(exc)}),file=sys.stderr);sys.exit(1)
