# SPDX-License-Identifier: GPL-2.0
# Copyright 2026 The HuaTuo Authors.

ifneq ($(KERNELRELEASE),)
obj-m += huatuo_hungtask_mock.o
ccflags-y += -I$(src) -I$(obj)
.DELETE_ON_ERROR:

# Use the build kernel's event layout, including fixed or dynamic comm fields.
$(obj)/hungtask_event.h: $(srctree)/include/trace/events/sched.h
	awk '/^TRACE_EVENT\(sched_process_hang,/ { found = 1 } \
		found { sub(/TRACE_EVENT\(sched_process_hang,/, "TRACE_EVENT(sched_process_hang_mock,"); print } \
		found && /^\);/ { complete = 1; exit } \
		END { if (!complete) exit 1 }' $< > $@

$(obj)/huatuo_hungtask_mock.o: $(obj)/hungtask_event.h
else
KERNEL_BUILD_DIR ?= /lib/modules/$(shell uname -r)/build

all:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) modules

clean:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) clean
endif
