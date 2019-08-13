// Copyright 2019 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"istio.io/istio/pilot/pkg/model"
)

// Helper function to remove an item or timeout and return nil if there are no pending pushes
func getWithTimeout(p *PushQueue) *XdsConnection {
	done := make(chan *XdsConnection)
	go func() {
		con, _ := p.Dequeue()
		done <- con
	}()
	select {
	case ret := <-done:
		return ret
	case <-time.After(time.Millisecond * 500):
		return nil
	}
}

func ExpectTimeout(t *testing.T, p *PushQueue) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		p.Dequeue()
		done <- struct{}{}
	}()
	select {
	case <-done:
		t.Fatalf("Expected timeout")
	case <-time.After(time.Millisecond * 500):
	}
}

func ExpectDequeue(t *testing.T, p *PushQueue, expected *XdsConnection) {
	t.Helper()
	result := make(chan *XdsConnection)
	go func() {
		con, _ := p.Dequeue()
		result <- con
	}()
	select {
	case got := <-result:
		if got != expected {
			t.Fatalf("Expected proxy %v, got %v", expected, got)
		}
	case <-time.After(time.Millisecond * 500):
		t.Fatalf("Timed out")
	}
}

func TestProxyQueue(t *testing.T) {
	proxies := make([]*XdsConnection, 0, 100)
	for p := 0; p < 100; p++ {
		proxies = append(proxies, &XdsConnection{ConID: fmt.Sprintf("proxy-%d", p)})
	}

	t.Run("simple add and remove", func(t *testing.T) {
		p := NewPushQueue()
		p.Enqueue(proxies[0], &PushEvent{})
		p.Enqueue(proxies[1], &PushEvent{})

		ExpectDequeue(t, p, proxies[0])
		ExpectDequeue(t, p, proxies[1])
	})

	t.Run("remove too many", func(t *testing.T) {
		p := NewPushQueue()
		p.Enqueue(proxies[0], &PushEvent{})

		ExpectDequeue(t, p, proxies[0])
		ExpectTimeout(t, p)
	})

	t.Run("add multiple times", func(t *testing.T) {
		p := NewPushQueue()
		p.Enqueue(proxies[0], &PushEvent{})
		p.Enqueue(proxies[1], &PushEvent{})
		p.Enqueue(proxies[0], &PushEvent{})

		ExpectDequeue(t, p, proxies[0])
		ExpectDequeue(t, p, proxies[1])
		ExpectTimeout(t, p)
	})

	t.Run("add and remove and markdone", func(t *testing.T) {
		p := NewPushQueue()
		p.Enqueue(proxies[0], &PushEvent{})
		ExpectDequeue(t, p, proxies[0])
		p.MarkDone(proxies[0])
		p.Enqueue(proxies[0], &PushEvent{})
		ExpectDequeue(t, p, proxies[0])
		ExpectTimeout(t, p)
	})

	t.Run("add and remove and add and markdone", func(t *testing.T) {
		p := NewPushQueue()
		p.Enqueue(proxies[0], &PushEvent{})
		ExpectDequeue(t, p, proxies[0])
		p.Enqueue(proxies[0], &PushEvent{})
		p.Enqueue(proxies[0], &PushEvent{})
		p.MarkDone(proxies[0])
		ExpectDequeue(t, p, proxies[0])
		ExpectTimeout(t, p)
	})

	t.Run("remove should block", func(t *testing.T) {
		p := NewPushQueue()
		wg := &sync.WaitGroup{}
		wg.Add(1)
		go func() {
			ExpectDequeue(t, p, proxies[0])
			wg.Done()
		}()
		time.Sleep(time.Millisecond * 50)
		p.Enqueue(proxies[0], &PushEvent{})
		wg.Wait()
	})

	t.Run("should merge PushEvent", func(t *testing.T) {
		p := NewPushQueue()
		firstTime := time.Now()
		p.Enqueue(proxies[0], &PushEvent{
			full:               false,
			edsUpdatedServices: map[string]struct{}{"foo": {}},
			start:              firstTime,
		})

		p.Enqueue(proxies[0], &PushEvent{
			full:               false,
			edsUpdatedServices: map[string]struct{}{"bar": {}},
			start:              firstTime.Add(time.Second),
		})
		_, info := p.Dequeue()

		if info.start != firstTime {
			t.Errorf("Expected start time to be %v, got %v", firstTime, info.start)
		}
		expectedEds := map[string]struct{}{"foo": {}, "bar": {}}
		if !reflect.DeepEqual(info.edsUpdatedServices, expectedEds) {
			t.Errorf("Expected edsUpdatedServices to be %v, got %v", expectedEds, info.edsUpdatedServices)
		}
		if info.full != false {
			t.Errorf("Expected full to be false, got true")
		}
	})

	t.Run("two removes, one should block one should return", func(t *testing.T) {
		p := NewPushQueue()
		wg := &sync.WaitGroup{}
		wg.Add(2)
		respChannel := make(chan *XdsConnection, 2)
		go func() {
			respChannel <- getWithTimeout(p)
			wg.Done()
		}()
		time.Sleep(time.Millisecond * 50)
		p.Enqueue(proxies[0], &PushEvent{})
		go func() {
			respChannel <- getWithTimeout(p)
			wg.Done()
		}()

		wg.Wait()
		timeouts := 0
		close(respChannel)
		for resp := range respChannel {
			if resp == nil {
				timeouts++
			}
		}
		if timeouts != 1 {
			t.Fatalf("Expected 1 timeout, got %v", timeouts)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		p := NewPushQueue()
		key := func(p *XdsConnection, eds string) string { return fmt.Sprintf("%s~%s", p.ConID, eds) }

		// We will trigger many pushes for eds services to each proxy. In the end we will expect
		// all of these to be dequeue, but order is not deterministic.
		expected := map[string]struct{}{}
		for eds := 0; eds < 100; eds++ {
			for _, pr := range proxies {
				expected[key(pr, fmt.Sprintf("%d", eds))] = struct{}{}
			}
		}
		go func() {
			for eds := 0; eds < 100; eds++ {
				for _, pr := range proxies {
					p.Enqueue(pr, &PushEvent{edsUpdatedServices: map[string]struct{}{
						fmt.Sprintf("%d", eds): {},
					}})
				}
			}
		}()

		done := make(chan struct{})
		mu := sync.RWMutex{}
		go func() {
			for {
				con, info := p.Dequeue()
				for eds := range info.edsUpdatedServices {
					mu.Lock()
					delete(expected, key(con, eds))
					mu.Unlock()
				}
				p.MarkDone(con)
				if len(expected) == 0 {
					done <- struct{}{}
				}
			}
		}()

		select {
		case <-done:
		case <-time.After(time.Second * 10):
			mu.RLock()
			defer mu.RUnlock()
			t.Fatalf("failed to get all updates, still pending: %v", len(expected))
		}
	})
}

func TestPushEventMerge(t *testing.T) {
	push0 := &model.PushContext{}
	// trivially different push contexts just for testing
	push1 := &model.PushContext{ProxyStatus: make(map[string]map[string]model.ProxyPushStatus)}

	var t0 time.Time
	t1 := t0.Add(time.Minute)

	cases := []struct {
		name   string
		left   *PushEvent
		right  *PushEvent
		merged *PushEvent
	}{
		{
			"left nil",
			nil,
			&PushEvent{push: push0, start: t1},
			&PushEvent{push: push0, start: t1},
		},
		{
			"right nil",
			&PushEvent{push: push0, start: t1},
			nil,
			&PushEvent{push: push0, start: t1},
		},
		{
			// Expect to keep left's start, right's push, and merge eds and full.
			"full merge",
			&PushEvent{
				edsUpdatedServices: map[string]struct{}{
					"ns1": {},
				},
				push:  push0,
				start: t0,
				full:  false,
			},
			&PushEvent{
				edsUpdatedServices: map[string]struct{}{
					"ns2": {},
				},
				push:  push1,
				start: t1,
				full:  true,
			},
			&PushEvent{
				edsUpdatedServices: nil, // full push ignores this field
				push:               push1,
				start:              t0,
				full:               true,
			},
		},
		{
			// Expect to keep left's start, right's push, and merge eds and full.
			"incremental merge",
			&PushEvent{
				edsUpdatedServices: map[string]struct{}{
					"ns1": {},
				},
				push:  push0,
				start: t0,
				full:  false,
			},
			&PushEvent{
				edsUpdatedServices: map[string]struct{}{
					"ns2": {},
				},
				push:  push1,
				start: t1,
				full:  false,
			},
			&PushEvent{
				edsUpdatedServices: map[string]struct{}{
					"ns1": {},
					"ns2": {},
				},
				push:  push1,
				start: t0,
				full:  false,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.left.Merge(tt.right)
			if !reflect.DeepEqual(tt.merged, got) {
				t.Fatalf("expected %v, got %v", tt.merged, got)
			}
		})
	}
}
