# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The HuaTuo Authors.

obj-m += huatuo_oom_mock.o
huatuo_oom_mock-y := oom_mock_kmod.o

KERNEL_BUILD_DIR ?= /lib/modules/$(shell uname -r)/build

all:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) modules

clean:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) clean
