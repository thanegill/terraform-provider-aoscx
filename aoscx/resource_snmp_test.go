package aoscx

import (
	"testing"
)

func TestSystemSnmpCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.resource("/system", `{"enable_snmpv3_only": false}`)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_system_snmp", map[string]interface{}{"snmpv3_only": true})
	state, errs := p.create("aoscx_system_snmp", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		`PATCH /system {"enable_snmpv3_only": true}`,
		"GET /system?attributes=enable_snmpv3_only",
		"GET /system?attributes=enable_snmpv3_only",
	)
	expectState(t, state, `{"id": "system", "snmpv3_only": true}`)
	p.expectNoChanges(t, "aoscx_system_snmp", state, config)

	config = p.config("aoscx_system_snmp", map[string]interface{}{"snmpv3_only": false})
	state, errs = p.apply("aoscx_system_snmp", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(), `PATCH /system {"enable_snmpv3_only": false}`)

	expectOK(t, "destroy", p.destroy("aoscx_system_snmp", state))
	expectRequests(t, sw.requests())

	imported, errs := p.importState("aoscx_system_snmp", "system")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"snmpv3_only": false}`)
	p.expectNoChanges(t, "aoscx_system_snmp", imported, config)
}

const (
	snmpAuthSecret = "auth pass phrase 1"
	snmpPrivSecret = "priv pass phrase 1"
	snmpUser       = "/system/snmpv3_users/monitor"
)

// snmpUserConfig is the SNMPv3 user's declared attributes with changes made,
// a nil one removed.
func snmpUserConfig(changes map[string]interface{}) map[string]interface{} {
	declared := map[string]interface{}{
		"name":                   "monitor",
		"auth_protocol":          "sha256",
		"priv_protocol":          "aes",
		"auth_pass_phrase_wo":    snmpAuthSecret,
		"priv_pass_phrase_wo":    snmpPrivSecret,
		"pass_phrase_wo_version": 1,
	}
	for name, value := range changes {
		if value == nil {
			delete(declared, name)
		} else {
			declared[name] = value
		}
	}
	return declared
}

const snmpUserCreated = `POST /system/snmpv3_users {"user_name": "monitor", "access_level": "ro",
	"auth_protocol": "sha256", "priv_protocol": "aes",
	"auth_pass_phrase": "auth pass phrase 1", "priv_pass_phrase": "priv pass phrase 1"}`

// The pass phrases are sent only when the user is created, and never kept
// in plan or state: the provider's checks of every plan, state and error
// fail on either.
func TestSnmpv3UserSendsItsPassPhrasesOnlyOnCreate(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	p.secret(snmpAuthSecret)
	p.secret(snmpPrivSecret)

	config := p.config("aoscx_snmpv3_user", snmpUserConfig(nil))
	state, errs := p.create("aoscx_snmpv3_user", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		snmpUserCreated,
		"GET "+snmpUser+"?attributes=access_level,auth_protocol,priv_protocol",
	)
	expectState(t, state, `{"id": "monitor", "name": "monitor", "access_level": "ro", "auth_protocol": "sha256",
		"priv_protocol": "aes", "auth_pass_phrase_wo": null, "priv_pass_phrase_wo": null, "pass_phrase_wo_version": 1}`)
	p.expectNoChanges(t, "aoscx_snmpv3_user", state, config)

	config = p.config("aoscx_snmpv3_user", snmpUserConfig(map[string]interface{}{"access_level": "rw"}))
	state, errs = p.apply("aoscx_snmpv3_user", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		`PATCH `+snmpUser+` {"access_level": "rw"}`,
		"GET "+snmpUser+"?selector=writable",
		"GET "+snmpUser+"?attributes=access_level,auth_protocol,priv_protocol",
	)
	expectState(t, state, `{"access_level": "rw"}`)

	config = p.config("aoscx_snmpv3_user", snmpUserConfig(map[string]interface{}{"access_level": "rw", "pass_phrase_wo_version": 2}))
	state, errs = p.apply("aoscx_snmpv3_user", state, config)
	expectOK(t, "a new pass_phrase_wo_version", errs)
	expectRequests(t, sw.writes(),
		"DELETE "+snmpUser,
		`POST /system/snmpv3_users {"user_name": "monitor", "access_level": "rw",
			"auth_protocol": "sha256", "priv_protocol": "aes",
			"auth_pass_phrase": "auth pass phrase 1", "priv_pass_phrase": "priv pass phrase 1"}`,
	)
	expectState(t, state, `{"pass_phrase_wo_version": 2}`)

	change, errs := p.plan("aoscx_snmpv3_user", state, p.config("aoscx_snmpv3_user",
		snmpUserConfig(map[string]interface{}{"access_level": "rw", "pass_phrase_wo_version": 2, "auth_protocol": "sha512"})))
	expectOK(t, "plan", errs)
	if !change.replace {
		t.Errorf("a new auth_protocol does not replace the user")
	}

	expectOK(t, "destroy", p.destroy("aoscx_snmpv3_user", state))
	expectRequests(t, sw.requests(), "DELETE "+snmpUser)
	expectAttributes(t, sw, snmpUser, "")
}

func TestSnmpv3UserCreateTakesBothPassPhrases(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	p.secret(snmpPrivSecret)
	_, errs := p.create("aoscx_snmpv3_user", p.config("aoscx_snmpv3_user", snmpUserConfig(map[string]interface{}{"auth_pass_phrase_wo": nil})))
	expectError(t, "create", errs, "SNMPv3 user monitor: creating it takes auth_pass_phrase_wo")
	expectRequests(t, sw.requests())
}

// A new pass_phrase_wo_version replaces the user, and the create it plans
// takes both pass phrases, so the plan fails before the destroy.
func TestSnmpv3UserReplacedWithoutPassPhrasesIsRefused(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	p.secret(snmpAuthSecret)
	p.secret(snmpPrivSecret)
	state, errs := p.create("aoscx_snmpv3_user", p.config("aoscx_snmpv3_user", snmpUserConfig(nil)))
	expectOK(t, "create", errs)
	sw.requests()
	_, errs = p.apply("aoscx_snmpv3_user", state, p.config("aoscx_snmpv3_user", snmpUserConfig(map[string]interface{}{
		"pass_phrase_wo_version": 2, "auth_pass_phrase_wo": nil, "priv_pass_phrase_wo": nil,
	})))
	expectError(t, "a replacement without pass phrases", errs, "SNMPv3 user monitor: creating it takes auth_pass_phrase_wo")
	expectRequests(t, sw.requests())
	expectAttributes(t, sw, snmpUser, `{"user_name": "monitor", "access_level": "ro", "auth_protocol": "sha256", "priv_protocol": "aes",
		"auth_pass_phrase": "auth pass phrase 1", "priv_pass_phrase": "priv pass phrase 1"}`)
}

// The fake answers a refused request with its body, pass phrases and all;
// the error must not repeat it.
func TestSnmpv3UserRefusedCreateDoesNotEchoThePassPhrases(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.refuse["POST /system/snmpv3_users"] = true
	p := configuredProvider(t, sw)
	p.secret(snmpAuthSecret)
	p.secret(snmpPrivSecret)
	_, errs := p.create("aoscx_snmpv3_user", p.config("aoscx_snmpv3_user", snmpUserConfig(nil)))
	expectError(t, "create", errs, "could not create SNMPv3 user monitor: POST /system/snmpv3_users answered HTTP 400")
}

func TestSnmpv3UserImport(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.member("/system/snmpv3_users", "monitor", `{"user_name": "monitor", "access_level": "ro",
		"auth_protocol": "sha256", "priv_protocol": "aes", "auth_pass_phrase": "AQBapEncrypted", "priv_pass_phrase": "AQBapEncrypted"}`)
	p := configuredProvider(t, sw)
	p.secret("AQBapEncrypted")
	imported, errs := p.importState("aoscx_snmpv3_user", "monitor")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"id": "monitor", "name": "monitor", "access_level": "ro", "auth_protocol": "sha256", "priv_protocol": "aes"}`)
	expectRequests(t, sw.requests(), "GET "+snmpUser+"?attributes=access_level,auth_protocol,priv_protocol")

	// The pass phrases are never compared, so they change nothing in a user
	// imported without them, as long as no pass_phrase_wo_version is
	// declared; declaring one replaces the user, sending them.
	change, errs := p.plan("aoscx_snmpv3_user", imported, p.config("aoscx_snmpv3_user", snmpUserConfig(map[string]interface{}{"pass_phrase_wo_version": nil})))
	expectOK(t, "plan", errs)
	if change.changes(imported) {
		t.Errorf("plan after import changes %v", attributes(change.planned))
	}
	change, errs = p.plan("aoscx_snmpv3_user", imported, p.config("aoscx_snmpv3_user", snmpUserConfig(nil)))
	expectOK(t, "plan with a version", errs)
	if !change.replace {
		t.Errorf("a pass_phrase_wo_version declared after import does not replace the user")
	}

	sw.member("/system/snmpv3_users", "monitor", `{"user_name": "monitor"}`)
	read, errs := p.read("aoscx_snmpv3_user", imported)
	expectOK(t, "read of unset settings", errs)
	expectState(t, read, `{"access_level": "ro", "auth_protocol": "none", "priv_protocol": "none"}`)
}
