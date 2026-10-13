# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The HuaTuo Authors.

KERNEL_BUILD_DIR ?= /lib/modules/$(shell uname -r)/build

.PHONY: all clean
all:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) modules

clean:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) clean
