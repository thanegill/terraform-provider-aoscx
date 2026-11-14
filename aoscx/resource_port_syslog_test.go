package aoscx

import (
	"testing"
)

const port1 = "/system/interfaces/1%2F1%2F1"

func TestPortSettingsCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource(port1, `{"description": "uplink", "user_config": {"admin": "up", "autoneg": "on", "speeds": "1000-full"}}`)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_port_settings", map[string]interface{}{"interface": "1/1/1", "mtu": 9000, "admin_state": "up"})
	state, errs := p.create("aoscx_port_settings", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET "+port1+"?attributes=user_config",
		"PATCH "+port1+` {"user_config": {"admin": "up", "autoneg": "on", "speeds": "1000-full", "mtu": 9000}}`,
		"GET "+port1+"?attributes=user_config",
		"GET "+port1+"?attributes=user_config",
	)
	expectState(t, state, `{"id": "1/1/1", "interface": "1/1/1", "mtu": 9000, "admin_state": "up"}`)

	config = p.config("aoscx_port_settings", map[string]interface{}{"interface": "1/1/1", "mtu": 9000, "admin_state": "down"})
	state, errs = p.apply("aoscx_port_settings", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(),
		"PATCH "+port1+` {"user_config": {"admin": "down", "autoneg": "on", "speeds": "1000-full", "mtu": 9000}}`)
	p.expectNoChanges(t, "aoscx_port_settings", state, config)

	expectOK(t, "destroy", p.destroy("aoscx_port_settings", state))
	expectRequests(t, sw.requests())
	expectAttributes(t, sw, port1, `{"description": "uplink", "user_config": {"admin": "down", "autoneg": "on", "speeds": "1000-full", "mtu": 9000}}`)

	imported, errs := p.importState("aoscx_port_settings", "1/1/1")
	expectOK(t, "import", errs)
	p.expectNoChanges(t, "aoscx_port_settings", imported, config)
	expectRequests(t, sw.requests(), "GET "+port1+"?attributes=user_config")
}

func TestPortSettingsReadUnsetAsMtu1500AndUp(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource(port1, `{"user_config": {}}`)
	p := configuredProvider(t, sw)
	imported, errs := p.importState("aoscx_port_settings", "1/1/1")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"mtu": 1500, "admin_state": "up"}`)
}

func TestPortSettingsOfAPortTheSwitchCannotReadIsAnError(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource(port1, `{"user_config": {}}`)
	p := configuredProvider(t, sw)
	state, errs := p.importState("aoscx_port_settings", "1/1/1")
	expectOK(t, "import", errs)
	sw.drop["GET "+port1] = true
	_, errs = p.read("aoscx_port_settings", state)
	expectError(t, "read", errs, "could not read port 1/1/1: no response to GET "+port1)
}

const syslogRemote = "/system/syslog_remotes/10.0.0.9"

func syslogConfig(changes map[string]interface{}) map[string]interface{} {
	declared := map[string]interface{}{
		"remote_host": "10.0.0.9", "transport": "udp", "port_number": 514, "severity": "info",
		"vrf": "default", "include_auditable_events": true, "disable": false,
	}
	for name, value := range changes {
		declared[name] = value
	}
	return declared
}

func TestSyslogRemoteCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_syslog_remote", syslogConfig(nil))
	state, errs := p.create("aoscx_syslog_remote", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		`POST /system/syslog_remotes {"remote_host": "10.0.0.9", "transport": "udp", "port_number": 514,
			"severity": "info", "vrf": "/rest/v10.16/system/vrfs/default", "include_auditable_events": true, "disable": false}`,
		"GET "+syslogRemote+"?selector=writable",
	)
	expectState(t, state, `{"id": "10.0.0.9", "remote_host": "10.0.0.9", "transport": "udp", "port_number": 514,
		"severity": "info", "vrf": "default", "include_auditable_events": true, "disable": false}`)
	p.expectNoChanges(t, "aoscx_syslog_remote", state, config)

	// The switch defaults the port from the transport, so a new transport
	// writes the port too.
	config = p.config("aoscx_syslog_remote", syslogConfig(map[string]interface{}{"transport": "tcp"}))
	state, errs = p.apply("aoscx_syslog_remote", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		"PATCH "+syslogRemote+` {"transport": "tcp", "port_number": 514}`,
		"GET "+syslogRemote+"?selector=writable",
	)

	config = p.config("aoscx_syslog_remote", syslogConfig(map[string]interface{}{"transport": "tcp", "vrf": "mgmt", "disable": true}))
	state, errs = p.apply("aoscx_syslog_remote", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(), "PATCH "+syslogRemote+` {"vrf": "/rest/v10.16/system/vrfs/mgmt", "disable": true}`)
	expectState(t, state, `{"vrf": "mgmt", "disable": true}`)

	// The switch may answer a VRF as its path rather than {name: path}.
	sw.member("/system/syslog_remotes", "10.0.0.9", `{"remote_host": "10.0.0.9", "transport": "tcp", "port_number": 514,
		"severity": "info", "vrf": "/rest/v10.16/system/vrfs/mgmt", "include_auditable_events": true, "disable": true}`)
	imported, errs := p.importState("aoscx_syslog_remote", "10.0.0.9")
	expectOK(t, "import", errs)
	p.expectNoChanges(t, "aoscx_syslog_remote", imported, config)
	sw.requests()

	expectOK(t, "destroy", p.destroy("aoscx_syslog_remote", state))
	expectRequests(t, sw.requests(), "DELETE "+syslogRemote)
	expectAttributes(t, sw, syslogRemote, "")
}

func TestSyslogRemoteReadsUnsetSettingsAsTheSwitchDefaultsThem(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.member("/system/syslog_remotes", "10.0.0.9", `{"remote_host": "10.0.0.9", "transport": "tls"}`)
	p := configuredProvider(t, sw)
	imported, errs := p.importState("aoscx_syslog_remote", "10.0.0.9")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"transport": "tls", "port_number": 6514, "severity": "info", "vrf": "default",
		"include_auditable_events": false, "disable": false}`)
}

func TestSyslogRemoteFailsWhenTheSwitchDoesNotHoldTheWrite(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.afterWrite = func(method string, path string) {
		sw.members["/system/syslog_remotes"]["10.0.0.9"]["severity"] = "err"
	}
	p := configuredProvider(t, sw)
	_, errs := p.create("aoscx_syslog_remote", p.config("aoscx_syslog_remote", syslogConfig(nil)))
	expectError(t, "create", errs, "its syslog remote 10.0.0.9 has severity err, not info")
}
