	_, err := manager.Create(CreateJobRequest{
		UserID:    "operator-2026",
		Container: "payment-worker",
		Host:      "huatuo-dev",
		JobType:   "oncpu",
		Args: &NewAgentTaskReq{
			TracerName:   "oncpu",
			TraceTimeout: 300,
			DataType:     "flamegraph",
		},
	})
	if err != nil {
		t.Fatalf("Create() error=%v, want nil", err)
	}

	// Simulate manager shutdown while job is still running.
	// Before the fix, this would cause a nil pointer dereference
	// because the defer block called err.Error() when err was nil.
	manager.Shutdown()

	// Wait for monitorJob goroutine to finish processing.
	time.Sleep(300 * time.Millisecond)

	// If we reach here without panic, the test passes.
	// Verify the job was marked as failed with a non-empty error message.
	lastSave := storage.saveCalls[len(storage.saveCalls)-1]
	if lastSave.Status != JobStatusFailed {
		t.Errorf("job.Status=%s, want %s", lastSave.Status, JobStatusFailed)
	}
	if lastSave.Error == "" {
		t.Errorf("job.Error is empty, want non-empty error message")
	}
}