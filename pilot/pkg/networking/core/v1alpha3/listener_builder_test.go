// Copyright 2018 Istio Authors
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

package v1alpha3

import (
	"strings"
	"testing"

	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/networking/plugin"
	"istio.io/istio/pkg/config/protocol"
)

type LdsEnv struct {
	configgen *ConfigGeneratorImpl
}

func getDefaultLdsEnv() *LdsEnv {
	listenerEnv := LdsEnv{
		configgen: NewConfigGenerator([]plugin.Plugin{&fakePlugin{}}),
	}
	return &listenerEnv
}

func getDefaultProxy() model.Proxy {
	return model.Proxy{
		Type:        model.SidecarProxy,
		IPAddresses: []string{"1.1.1.1"},
		ID:          "v0.default",
		DNSDomain:   "default.example.org",
		Metadata: map[string]string{
			model.NodeMetadataConfigNamespace: "not-default",
			"ISTIO_VERSION":                   "1.3",
		},
		IstioVersion:    model.ParseIstioVersion("1.3"),
		ConfigNamespace: "not-default",
	}
}

func setNilSidecarOnProxy(proxy *model.Proxy, pushContext *model.PushContext) {
	proxy.SidecarScope = model.DefaultSidecarScopeForNamespace(pushContext, "not-default")
}

func TestListenerBuilder(t *testing.T) {
	// prepare
	t.Helper()
	ldsEnv := getDefaultLdsEnv()
	service := buildService("test.com", wildcardIP, protocol.HTTP, tnow)
	services := []*model.Service{service}

	env := buildListenerEnv(services)

	if err := env.PushContext.InitContext(&env); err != nil {
		t.Fatalf("init push context error: %s", err.Error())
	}
	instances := make([]*model.ServiceInstance, len(services))
	for i, s := range services {
		instances[i] = &model.ServiceInstance{
			Service:  s,
			Endpoint: buildEndpoint(s),
		}
	}
	proxy := getDefaultProxy()
	proxy.ServiceInstances = instances
	setNilSidecarOnProxy(&proxy, env.PushContext)

	builder := NewListenerBuilder(&proxy)
	listeners := builder.buildSidecarInboundListeners(ldsEnv.configgen, &env, &proxy, env.PushContext).
		getListeners()

	// the listener for app
	if len(listeners) != 1 {
		t.Fatalf("expected %d listeners, found %d", 1, len(listeners))
	}
	p := service.Ports[0].Protocol
	if p != protocol.HTTP && isHTTPListener(listeners[0]) {
		t.Fatal("expected TCP listener, found HTTP")
	} else if p == protocol.HTTP && !isHTTPListener(listeners[0]) {
		t.Fatal("expected HTTP listener, found TCP")
	}
	verifyInboundHTTPListenerServerName(t, listeners[0])
	if isHTTPListener(listeners[0]) {
		verifyInboundHTTPListenerCertDetails(t, listeners[0])
	}

	verifyInboundEnvoyListenerNumber(t, listeners[0])
}

func TestVirtualListenerBuilder(t *testing.T) {
	// prepare
	t.Helper()
	ldsEnv := getDefaultLdsEnv()
	service := buildService("test.com", wildcardIP, protocol.HTTP, tnow)
	services := []*model.Service{service}

	env := buildListenerEnv(services)
	if err := env.PushContext.InitContext(&env); err != nil {
		t.Fatalf("init push context error: %s", err.Error())
	}
	instances := make([]*model.ServiceInstance, len(services))
	for i, s := range services {
		instances[i] = &model.ServiceInstance{
			Service:  s,
			Endpoint: buildEndpoint(s),
		}
	}
	proxy := getDefaultProxy()
	proxy.ServiceInstances = instances
	setNilSidecarOnProxy(&proxy, env.PushContext)

	builder := NewListenerBuilder(&proxy)
	listeners := builder.buildSidecarInboundListeners(ldsEnv.configgen, &env, &proxy, env.PushContext).
		buildVirtualOutboundListener(ldsEnv.configgen, &env, &proxy, env.PushContext).
		getListeners()

	// app port listener and virtual inbound listener
	if len(listeners) != 2 {
		t.Fatalf("expected %d listeners, found %d", 2, len(listeners))
	}

	// The rest attributes are verified in other tests
	verifyInboundHTTPListenerServerName(t, listeners[0])

	if !strings.HasPrefix(listeners[1].Name, VirtualOutboundListenerName) {
		t.Fatalf("expect virtual listener, found %s", listeners[1].Name)
	} else {
		t.Logf("found virtual listener: %s", listeners[1].Name)
	}

}

func setInboundCaptureAllOnThisNode(proxy *model.Proxy) {
	proxy.Metadata[model.NodeMetadataInterceptionMode] = "REDIRECT"
	proxy.Metadata[model.IstioIncludeInboundPorts] = model.AllPortsLiteral
}

func TestVirtualInboundListenerBuilder(t *testing.T) {
	// prepare
	t.Helper()
	ldsEnv := getDefaultLdsEnv()
	service := buildService("test.com", wildcardIP, protocol.HTTP, tnow)
	services := []*model.Service{service}

	env := buildListenerEnv(services)
	if err := env.PushContext.InitContext(&env); err != nil {
		t.Fatalf("init push context error: %s", err.Error())
	}
	instances := make([]*model.ServiceInstance, len(services))
	for i, s := range services {
		instances[i] = &model.ServiceInstance{
			Service:  s,
			Endpoint: buildEndpoint(s),
		}
	}

	proxy := getDefaultProxy()
	proxy.ServiceInstances = instances
	setInboundCaptureAllOnThisNode(&proxy)
	setNilSidecarOnProxy(&proxy, env.PushContext)

	builder := NewListenerBuilder(&proxy)
	listeners := builder.buildSidecarInboundListeners(ldsEnv.configgen, &env, &proxy, env.PushContext).
		buildVirtualOutboundListener(ldsEnv.configgen, &env, &proxy, env.PushContext).
		buildVirtualInboundListener(&env, &proxy).
		getListeners()

	// app port listener and virtual inbound listener
	if len(listeners) != 3 {
		t.Fatalf("expected %d listeners, found %d", 3, len(listeners))
	}

	// The rest attributes are verified in other tests
	verifyInboundHTTPListenerServerName(t, listeners[0])

	if !strings.HasPrefix(listeners[1].Name, VirtualOutboundListenerName) {
		t.Fatalf("expect virtual listener, found %s", listeners[1].Name)
	} else {
		t.Logf("found virtual listener: %s", listeners[1].Name)
	}

	if !strings.HasPrefix(listeners[2].Name, VirtualInboundListenerName) {
		t.Fatalf("expect virtual listener, found %s", listeners[2].Name)
	} else {
		t.Logf("found virtual inbound listener: %s", listeners[2].Name)
	}

	l := listeners[2]
	// 2 is the passthrough tcp filter chains one for ipv4 and one for ipv6
	if len(l.FilterChains) != len(listeners[0].FilterChains)+2 {
		t.Fatalf("expect virtual listener has %d filter chains as the sum of 2nd level listeners "+
			"plus the 2 fallthrough filter chains, found %d", len(listeners[0].FilterChains)+2, len(l.FilterChains))
	}

	byListenerName := map[string]int{}

	for _, fc := range l.FilterChains {
		byListenerName[fc.Metadata.FilterMetadata[PilotMetaKey].Fields["original_listener_name"].GetStringValue()]++
	}

	for k, v := range byListenerName {
		if k == VirtualInboundListenerName && v != 2 {
			t.Fatalf("expect virtual listener has 2 passthrough listeners, found %d", v)
		}
		if k == listeners[0].Name && v != len(listeners[0].FilterChains) {
			t.Fatalf("expect virtual listener has %d filter chains from listener %s, found %d", len(listeners[0].FilterChains), l.Name, v)
		}
	}
}
