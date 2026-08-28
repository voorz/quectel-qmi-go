package netcfg

import (
	"sync"
	"testing"
)

func TestGetConfiguratorIsSafeUnderConcurrentFirstUse(t *testing.T) {
	original := GetConfigurator()
	t.Cleanup(func() { SetConfigurator(original) })
	SetConfigurator(nil)

	var wg sync.WaitGroup
	got := make([]NetworkConfigurator, 16)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = GetConfigurator()
		}(i)
	}
	wg.Wait()

	for i, configurator := range got {
		if configurator == nil {
			t.Fatalf("call %d returned nil configurator", i)
		}
		if configurator != got[0] {
			t.Fatalf("call %d returned a different configurator instance", i)
		}
	}
}

func TestSetConfiguratorConcurrentWithGet(t *testing.T) {
	original := GetConfigurator()
	t.Cleanup(func() { SetConfigurator(original) })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			SetConfigurator(GetPlatformConfigurator())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = GetConfigurator()
		}
	}()
	wg.Wait()
}
