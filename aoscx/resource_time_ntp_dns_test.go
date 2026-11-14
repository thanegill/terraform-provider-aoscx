package aoscx

import (
	"testing"
)

func TestSystemTimeCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system", `{"timezone": "UTC", "ntp_config": {"enable": false, "authentication_enable": false}}`)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_system_time", map[string]interface{}{"timezone": "America/Los_Angeles", "ntp_enable": true})
	state, errs := p.create("aoscx_system_time", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET /system?attributes=ntp_config",
		`PATCH /system {"timezone": "America/Los_Angeles", "ntp_config": {"enable": true, "authentication_enable": false}}`,
		"GET /system?attributes=ntp_config,timezone",
		"GET /system?attributes=ntp_config,timezone",
	)
	expectState(t, state, `{"id": "system", "timezone": "America/Los_Angeles", "ntp_enable": true}`)

	config = p.config("aoscx_system_time", map[string]interface{}{"timezone": "Europe/Berlin", "ntp_enable": true})
	state, errs = p.apply("aoscx_system_time", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		`PATCH /system {"timezone": "Europe/Berlin"}`,
		"GET /system?attributes=timezone",
		"GET /system?attributes=ntp_config,timezone",
	)
	p.expectNoChanges(t, "aoscx_system_time", state, config)

	expectOK(t, "destroy", p.destroy("aoscx_system_time", state))
	expectRequests(t, sw.requests())

	imported, errs := p.importState("aoscx_system_time", "system")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"id": "system", "timezone": "Europe/Berlin", "ntp_enable": true}`)
	p.expectNoChanges(t, "aoscx_system_time", imported, config)
	expectRequests(t, sw.writes())
}

func TestSystemTimeReadsUnsetAsUtcWithoutNtp(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system", `{}`)
	p := configuredProvider(t, sw)
	imported, errs := p.importState("aoscx_system_time", "system")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"timezone": "UTC", "ntp_enable": false}`)
}

const ntpServer = "/system/vrfs/default/ntp_associations/ntp.example.com"

func TestNtpServerCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)

	declared := map[string]interface{}{
		"address": "ntp.example.com", "burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": true,
	}
	config := p.config("aoscx_ntp_server", declared)
	state, errs := p.create("aoscx_ntp_server", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		`POST /system/vrfs/default/ntp_associations {"address": "ntp.example.com", "association_attributes":
			{"burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": true}}`,
		"GET "+ntpServer+"?selector=writable",
	)
	expectState(t, state, `{"id": "ntp.example.com", "address": "ntp.example.com", "burst_mode": "iburst",
		"minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": true}`)

	// The authentication key is not managed, and an update keeps it.
	sw.member("/system/vrfs/default/ntp_associations", "ntp.example.com", `{"address": "ntp.example.com",
		"association_attributes": {"burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": true, "key_id": 7}}`)
	declared["prefer"] = false
	config = p.config("aoscx_ntp_server", declared)
	state, errs = p.apply("aoscx_ntp_server", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		"GET "+ntpServer+"?selector=writable",
		"PATCH "+ntpServer+` {"association_attributes":
			{"burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": false, "key_id": 7}}`,
		"GET "+ntpServer+"?selector=writable",
		"GET "+ntpServer+"?selector=writable",
	)
	p.expectNoChanges(t, "aoscx_ntp_server", state, config)

	change, errs := p.plan("aoscx_ntp_server", state, p.config("aoscx_ntp_server", map[string]interface{}{
		"address": "ntp2.example.com", "burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": false,
	}))
	expectOK(t, "plan", errs)
	if !change.replace {
		t.Errorf("a new address does not replace the server")
	}

	imported, errs := p.importState("aoscx_ntp_server", "ntp.example.com")
	expectOK(t, "import", errs)
	p.expectNoChanges(t, "aoscx_ntp_server", imported, config)
	sw.requests()

	expectOK(t, "destroy", p.destroy("aoscx_ntp_server", state))
	expectRequests(t, sw.requests(), "DELETE "+ntpServer)
	expectAttributes(t, sw, ntpServer, "")

	expectOK(t, "destroy of a server already gone", p.destroy("aoscx_ntp_server", state))
	gone, errs := p.read("aoscx_ntp_server", state)
	expectOK(t, "read of a server gone", errs)
	if !gone.IsNull() {
		t.Errorf("a server the switch lacks is kept: %v", attributes(gone))
	}
}

func TestNtpServerFailsWhenTheSwitchDoesNotHoldTheWrite(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.afterWrite = func(method string, path string) {
		for _, member := range sw.members["/system/vrfs/default/ntp_associations"] {
			member["association_attributes"].(map[string]interface{})["minpoll"] = 5
		}
	}
	p := configuredProvider(t, sw)
	_, errs := p.create("aoscx_ntp_server", p.config("aoscx_ntp_server", map[string]interface{}{
		"address": "ntp.example.com", "burst_mode": "iburst", "minpoll": 4, "maxpoll": 6, "ntp_version": 4, "prefer": true,
	}))
	expectError(t, "create", errs, "its NTP server ntp.example.com has minpoll 5, not 4")
}

func TestVrfDnsCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system/vrfs/default", `{"dns_name_servers": {}, "dns_domain_name": null, "name": "default"}`)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_vrf_dns", map[string]interface{}{
		"vrf": "default", "name_servers": []string{"10.0.0.53", "10.0.0.54"}, "domain_name": "example.com",
	})
	state, errs := p.create("aoscx_vrf_dns", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET /system/vrfs/default?attributes=dns_name_servers",
		"GET /system/vrfs/default?attributes=dns_name_servers",
		`PATCH /system/vrfs/default {"dns_domain_name": "example.com", "dns_name_servers": {"0": "10.0.0.53", "1": "10.0.0.54"}}`,
		"GET /system/vrfs/default?attributes=dns_domain_name,dns_name_servers",
		"GET /system/vrfs/default?attributes=dns_domain_name,dns_name_servers",
	)
	expectState(t, state, `{"id": "default", "vrf": "default", "name_servers": ["10.0.0.53", "10.0.0.54"], "domain_name": "example.com"}`)

	config = p.config("aoscx_vrf_dns", map[string]interface{}{
		"vrf": "default", "name_servers": []string{"10.0.0.53", "10.0.0.55", "10.0.0.56"}, "domain_name": "example.com",
	})
	state, errs = p.apply("aoscx_vrf_dns", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(),
		`PATCH /system/vrfs/default {"dns_name_servers": {"0": "10.0.0.53", "1": "10.0.0.55", "2": "10.0.0.56"}}`)
	expectState(t, state, `{"name_servers": ["10.0.0.53", "10.0.0.55", "10.0.0.56"]}`)
	p.expectNoChanges(t, "aoscx_vrf_dns", state, config)

	_, errs = p.plan("aoscx_vrf_dns", state, p.config("aoscx_vrf_dns", map[string]interface{}{
		"vrf": "default", "name_servers": []string{"10.0.0.53"}, "domain_name": "example.com",
	}))
	expectError(t, "a plan dropping name servers", errs,
		"has name servers at priorities nothing declares: 10.0.0.55, 10.0.0.56. A PATCH cannot remove one")

	expectOK(t, "destroy", p.destroy("aoscx_vrf_dns", state))
	expectRequests(t, sw.requests())

	imported, errs := p.importState("aoscx_vrf_dns", "default")
	expectOK(t, "import", errs)
	p.expectNoChanges(t, "aoscx_vrf_dns", imported, config)
	expectRequests(t, sw.writes())
}

// A create over a switch with more name servers than declared would leave
// the extra ones, which a PATCH cannot remove, so it is refused before any
// write.
func TestVrfDnsCreateRefusesNameServersNothingDeclares(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system/vrfs/default", `{"dns_name_servers": {"0": "10.0.0.1", "1": "10.0.0.2", "2": "10.0.0.3"}, "dns_domain_name": null}`)
	p := configuredProvider(t, sw)
	state, errs := p.create("aoscx_vrf_dns", p.config("aoscx_vrf_dns", map[string]interface{}{
		"vrf": "default", "name_servers": []string{"10.0.0.53"}, "domain_name": "example.com",
	}))
	expectError(t, "create", errs, "the switch's DNS client of VRF default has name servers at priorities nothing declares: "+
		"10.0.0.2, 10.0.0.3. A PATCH cannot remove one")
	expectRequests(t, sw.requests(), "GET /system/vrfs/default?attributes=dns_name_servers")
	if !state.IsNull() {
		t.Errorf("a refused create left %v", attributes(state))
	}
}

func TestVrfDnsRefusesNameServerPriorityGaps(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system/vrfs/default", `{"dns_name_servers": {"0": "10.0.0.53", "2": "10.0.0.55"}, "dns_domain_name": "example.com"}`)
	p := configuredProvider(t, sw)
	_, errs := p.importState("aoscx_vrf_dns", "default")
	expectError(t, "import", errs, "has name server 10.0.0.55 at priority 2, with none at 1")
}

func TestVrfDnsOfAVrfTheSwitchLacksIsAnError(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	state, errs := p.importState("aoscx_vrf_dns", "mgmt")
	expectError(t, "import", errs, "could not read the DNS client of VRF mgmt")
	if !state.IsNull() {
		t.Errorf("imported %v", attributes(state))
	}
}
