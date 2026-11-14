package aoscx

import (
	"sync"
	"testing"
)

// systemWithOtherConfig is a /system whose other_config holds a key no
// resource manages, which every write must keep.
func systemWithOtherConfig(sw *fakeSwitch) {
	sw.resource("/system", `{"other_config": {"unmanaged": "kept", "banner": "old"}, "timezone": "UTC"}`)
}

var allTlvs = []string{"dot1_link_aggregation", "mgmt_addr", "org_specific", "port_desc", "port_vlan", "port_vlan_name", "sys_cap", "sys_desc", "sys_name"}

func TestBannerCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	systemWithOtherConfig(sw)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_banner", map[string]interface{}{"motd": "Authorized use only", "exec": ""})
	state, errs := p.create("aoscx_banner", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET /system?attributes=other_config",
		`PATCH /system {"other_config": {"unmanaged": "kept", "banner": "Authorized use only", "banner_exec": ""}}`,
		"GET /system?attributes=other_config",
		"GET /system?attributes=other_config",
	)
	expectState(t, state, `{"id": "system", "motd": "Authorized use only", "exec": ""}`)

	config = p.config("aoscx_banner", map[string]interface{}{"motd": "Authorized use only", "exec": "Welcome"})
	state, errs = p.apply("aoscx_banner", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		"GET /system?attributes=other_config",
		`PATCH /system {"other_config": {"unmanaged": "kept", "banner": "Authorized use only", "banner_exec": "Welcome"}}`,
		"GET /system?attributes=other_config",
		"GET /system?attributes=other_config",
	)
	expectState(t, state, `{"motd": "Authorized use only", "exec": "Welcome"}`)
	p.expectNoChanges(t, "aoscx_banner", state, config)

	expectOK(t, "destroy", p.destroy("aoscx_banner", state))
	expectRequests(t, sw.requests())
	expectAttributes(t, sw, "/system", `{"other_config": {"unmanaged": "kept", "banner": "Authorized use only", "banner_exec": "Welcome"}, "timezone": "UTC"}`)

	imported, errs := p.importState("aoscx_banner", "system")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"id": "system", "motd": "Authorized use only", "exec": "Welcome"}`)
	p.expectNoChanges(t, "aoscx_banner", imported, config)
	expectRequests(t, sw.writes())

	_, errs = p.importState("aoscx_banner", "banner")
	expectError(t, "import by another id", errs, `import this resource by the id "system"`)
}

func TestBannerFailsWhenTheSwitchDoesNotHoldTheWrite(t *testing.T) {
	sw := newFakeSwitch(t)
	systemWithOtherConfig(sw)
	sw.afterWrite = func(method string, path string) {
		sw.resources["/system"]["other_config"] = map[string]interface{}{"banner": "something else"}
	}
	p := configuredProvider(t, sw)
	_, errs := p.create("aoscx_banner", p.config("aoscx_banner", map[string]interface{}{"motd": "hi", "exec": ""}))
	expectError(t, "create", errs, "the switch took PATCH /system, but its other_config is")
}

func TestLldpCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	systemWithOtherConfig(sw)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_lldp", map[string]interface{}{
		"tx_interval": 60,
		"advertise":   []string{"sys_name", "sys_desc", "mgmt_addr"},
	})
	state, errs := p.create("aoscx_lldp", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET /system?attributes=other_config",
		`PATCH /system {"other_config": {"unmanaged": "kept", "banner": "old",
			"lldp_disable": false, "lldp_tx_interval": 60, "lldp_hold": 4, "lldp_txdelay": 2, "lldp_reinit": 2,
			"lldp_trap_enable": false,
			"lldp_tlv_dot1_link_aggregation_enable": false, "lldp_tlv_mgmt_addr_enable": true,
			"lldp_tlv_org_specific_enable": false, "lldp_tlv_port_desc_enable": false,
			"lldp_tlv_port_vlan_enable": false, "lldp_tlv_port_vlan_name_enable": false,
			"lldp_tlv_sys_cap_enable": false, "lldp_tlv_sys_desc_enable": true, "lldp_tlv_sys_name_enable": true}}`,
		"GET /system?attributes=other_config",
		"GET /system?attributes=other_config",
	)
	expectState(t, state, `{"id": "system", "enable": true, "tx_interval": 60, "hold_multiplier": 4, "tx_delay": 2,
		"reinit_delay": 2, "trap_enable": false, "advertise": ["mgmt_addr", "sys_desc", "sys_name"]}`)
	p.expectNoChanges(t, "aoscx_lldp", state, config)

	config = p.config("aoscx_lldp", map[string]interface{}{
		"tx_interval": 60,
		"trap_enable": true,
		"advertise":   []string{"sys_name", "sys_desc", "mgmt_addr"},
	})
	state, errs = p.apply("aoscx_lldp", state, config)
	expectOK(t, "update", errs)
	writes := sw.writes()
	expectRequests(t, writes, `PATCH /system {"other_config": {"unmanaged": "kept", "banner": "old",
			"lldp_disable": false, "lldp_tx_interval": 60, "lldp_hold": 4, "lldp_txdelay": 2, "lldp_reinit": 2,
			"lldp_trap_enable": true,
			"lldp_tlv_dot1_link_aggregation_enable": false, "lldp_tlv_mgmt_addr_enable": true,
			"lldp_tlv_org_specific_enable": false, "lldp_tlv_port_desc_enable": false,
			"lldp_tlv_port_vlan_enable": false, "lldp_tlv_port_vlan_name_enable": false,
			"lldp_tlv_sys_cap_enable": false, "lldp_tlv_sys_desc_enable": true, "lldp_tlv_sys_name_enable": true}}`)
	expectState(t, state, `{"trap_enable": true}`)

	expectOK(t, "destroy", p.destroy("aoscx_lldp", state))
	expectRequests(t, sw.requests())

	imported, errs := p.importState("aoscx_lldp", "system")
	expectOK(t, "import", errs)
	p.expectNoChanges(t, "aoscx_lldp", imported, config)
	expectRequests(t, sw.writes())
}

func TestLldpReadsUnsetSettingsAsTheSwitchDefaultsThem(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system", `{"other_config": null}`)
	p := configuredProvider(t, sw)
	imported, errs := p.importState("aoscx_lldp", "system")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"enable": true, "tx_interval": 30, "hold_multiplier": 4, "tx_delay": 2, "reinit_delay": 2, "trap_enable": false}`)
	if got := len(attributes(imported)["advertise"].([]interface{})); got != len(allTlvs) {
		t.Errorf("advertises %d TLVs, not all %d", got, len(allTlvs))
	}
}

// Both banner and LLDP send /system's other_config whole, from a read just
// before the PATCH, and tofu applies them at once. The fake answers the
// first of those reads only after a second one has been read, so without
// the lock both read the map before either PATCH and the later PATCH puts
// back what the earlier one changed.
func TestBannerAndLldpAppliedAtOnceKeepEachOthersKeys(t *testing.T) {
	sw := newFakeSwitch(t)
	systemWithOtherConfig(sw)
	sw.holdRead = "GET /system?attributes=other_config"
	p := configuredProvider(t, sw)

	var wg sync.WaitGroup
	errs := make([]string, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = p.create("aoscx_banner", p.config("aoscx_banner", map[string]interface{}{"motd": "hi", "exec": "there"}))
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = p.create("aoscx_lldp", p.config("aoscx_lldp", map[string]interface{}{"enable": false, "advertise": []string{}}))
	}()
	wg.Wait()
	for _, e := range errs {
		if e != "" {
			t.Errorf("apply: %s", e)
		}
	}
	config := decodeObject(sw.attributes("/system"))["other_config"].(map[string]interface{})
	for key, want := range map[string]interface{}{
		"unmanaged":    "kept",
		"banner":       "hi",
		"banner_exec":  "there",
		"lldp_disable": true,
	} {
		if config[key] != want {
			t.Errorf("other_config has %s = %v, not %v: %s", key, config[key], want, canonicalJSON(config))
		}
	}
}
