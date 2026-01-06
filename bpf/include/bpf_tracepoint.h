#ifndef __BPF_TRACEPOINT_H__
#define __BPF_TRACEPOINT_H__

static __always_inline char *__data_loc_address(char *ctx, u32 __data_loc)
{
	return ((char *)ctx + (__data_loc & 0xffff));
}

#endif /* __BPF_TRACEPOINT_H__ */
