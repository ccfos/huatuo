p "MEMRAY: Attached to process."

set unwindonsignal on
sharedlibrary libc
sharedlibrary libdl
sharedlibrary musl
sharedlibrary libpython
info sharedlibrary

# Compatibility: allow older Pythons by skipping PyMem_* probe that requires 3.7+
set scheduler-locking on
call (int)Py_AddPendingCall(&PyCallable_Check, (void*)0)

# When updating this list, also update the "commands" call below,
# and the breakpoints hardcoded for lldb in attach.py
b malloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b calloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b realloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b free
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyMem_Malloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyMem_Calloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyMem_Realloc
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyMem_Free
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyErr_CheckSignals
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
b PyCallable_Check
commands
    bt
    disable breakpoints
    delete breakpoints
    call (void*)dlopen($libpath, $rtld_now)
    p (char*)dlerror()
    eval "sharedlibrary %s", $libpath
    p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"
end
set scheduler-locking off
continue

