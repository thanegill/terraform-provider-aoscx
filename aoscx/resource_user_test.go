package aoscx

import (
	"testing"
)

const (
	userSecret = "user password 1"
	opsUser    = "/system/users/ops"
	keyA       = "ssh-ed25519 AAAAkeyA"
	keyB       = "ssh-rsa AAAAkeyB"
	keyC       = "ssh-ed25519 AAAAkeyC"
)

// userConfig is the user's declared attributes with changes made, a nil one
// removed.
func userConfig(changes map[string]interface{}) map[string]interface{} {
	declared := map[string]interface{}{
		"name":                "ops",
		"group":               "administrators",
		"authorized_keys":     []string{keyB, keyA},
		"password_wo":         userSecret,
		"password_wo_version": 1,
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

const userRead = "GET " + opsUser + "?attributes=authorized_keys,management_interface,user_group"

// The password is sent on create and when password_wo_version changes, and
// never kept in plan or state: the provider's checks of every plan, state
// and error fail on it.
func TestUserCreateUpdateDeleteImport(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	p.secret(userSecret)

	config := p.config("aoscx_user", userConfig(nil))
	state, errs := p.create("aoscx_user", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		`POST /system/users {"name": "ops", "user_group": "/rest/v10.16/system/user_groups/administrators",
			"authorized_keys": {"1": "ssh-ed25519 AAAAkeyA", "2": "ssh-rsa AAAAkeyB"}, "password": "user password 1"}`,
		userRead,
	)
	expectState(t, state, `{"id": "ops", "name": "ops", "group": "administrators", "password_wo": null,
		"password_wo_version": 1, "disabled_management_interfaces": []}`)
	p.expectNoChanges(t, "aoscx_user", state, config)

	// A key added at the CLI took number 3 and keyB's number 2 is gone, so
	// the keys added take the free numbers 2 and 4.
	sw.member("/system/users", "ops", `{"name": "ops",
		"user_group": {"administrators": "/rest/v10.16/system/user_groups/administrators"},
		"authorized_keys": {"1": "ssh-ed25519 AAAAkeyA", "3": "ssh-ed25519 AAAAkeyD laptop"}}`)
	state, errs = p.read("aoscx_user", state)
	expectOK(t, "read", errs)
	expectState(t, state, `{"authorized_keys": ["ssh-ed25519 AAAAkeyA", "ssh-ed25519 AAAAkeyD"]}`)
	sw.requests()

	config = p.config("aoscx_user", userConfig(map[string]interface{}{"authorized_keys": []string{keyA, keyB, keyC, "ssh-ed25519 AAAAkeyD"}}))
	state, errs = p.apply("aoscx_user", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.requests(),
		"GET "+opsUser+"?attributes=authorized_keys",
		"GET "+opsUser+"?attributes=authorized_keys",
		"PATCH "+opsUser+` {"authorized_keys": {"1": "ssh-ed25519 AAAAkeyA", "2": "ssh-ed25519 AAAAkeyC",
			"3": "ssh-ed25519 AAAAkeyD laptop", "4": "ssh-rsa AAAAkeyB"}}`,
		"GET "+opsUser+"?attributes=authorized_keys",
		userRead,
	)
	p.expectNoChanges(t, "aoscx_user", state, config)

	config = p.config("aoscx_user", userConfig(map[string]interface{}{
		"authorized_keys": []string{keyA, keyB, keyC, "ssh-ed25519 AAAAkeyD"}, "password_wo": "user password 2", "password_wo_version": 2,
	}))
	p.secret("user password 2")
	state, errs = p.apply("aoscx_user", state, config)
	expectOK(t, "a new password_wo_version", errs)
	expectRequests(t, sw.requests(), "PATCH "+opsUser+` {"password": "user password 2"}`, userRead)
	expectState(t, state, `{"password_wo_version": 2, "password_wo": null}`)

	change, errs := p.plan("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{
		"authorized_keys": []string{keyA, keyB, keyC, "ssh-ed25519 AAAAkeyD"}, "password_wo": "user password 2", "password_wo_version": 2,
		"group": "operators",
	})))
	expectOK(t, "plan", errs)
	if !change.replace {
		t.Errorf("a new group does not replace the user")
	}

	imported, errs := p.importState("aoscx_user", "ops")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"id": "ops", "name": "ops", "group": "administrators", "password_wo": null, "password_wo_version": null}`)
	change, errs = p.plan("aoscx_user", imported, p.config("aoscx_user", userConfig(map[string]interface{}{
		"authorized_keys": []string{keyA, keyB, keyC, "ssh-ed25519 AAAAkeyD"}, "password_wo_version": nil,
	})))
	expectOK(t, "plan after import", errs)
	if change.changes(imported) {
		t.Errorf("plan after import changes %v", attributes(change.planned))
	}
	sw.requests()

	expectOK(t, "destroy", p.destroy("aoscx_user", state))
	expectRequests(t, sw.requests(), "DELETE "+opsUser)
	expectAttributes(t, sw, opsUser, "")
	gone, errs := p.read("aoscx_user", state)
	expectOK(t, "read of a user gone", errs)
	if !gone.IsNull() {
		t.Errorf("a user the switch lacks is kept: %v", attributes(gone))
	}
}

func TestUserReplacedWithANewGroupTakesThePassword(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	p.secret(userSecret)
	state, errs := p.create("aoscx_user", p.config("aoscx_user", userConfig(map[string]interface{}{"authorized_keys": []string{keyA}})))
	expectOK(t, "create", errs)
	sw.requests()
	_, errs = p.apply("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{
		"authorized_keys": []string{keyA}, "group": "operators",
	})))
	expectOK(t, "replace", errs)
	expectRequests(t, sw.writes(),
		"DELETE "+opsUser,
		`POST /system/users {"name": "ops", "user_group": "/rest/v10.16/system/user_groups/operators",
			"authorized_keys": {"1": "ssh-ed25519 AAAAkeyA"}, "password": "user password 1"}`,
	)
}

func TestUserCreateTakesAPassword(t *testing.T) {
	sw := newFakeSwitch(t)
	p := configuredProvider(t, sw)
	_, errs := p.plan("aoscx_user", p.none("aoscx_user"), p.config("aoscx_user", userConfig(map[string]interface{}{"password_wo": nil})))
	expectError(t, "plan", errs, "user ops: creating it takes password_wo")
	expectRequests(t, sw.requests())
}

func TestUserRefusals(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.member("/system/users", "ops", `{"name": "ops",
		"user_group": {"administrators": "/rest/v10.16/system/user_groups/administrators"},
		"authorized_keys": {"1": "ssh-ed25519 AAAAkeyA", "2": "ssh-rsa AAAAkeyB"}}`)
	sw.refuse["PATCH "+opsUser] = true
	p := configuredProvider(t, sw)
	p.secret(userSecret)
	state, errs := p.importState("aoscx_user", "ops")
	expectOK(t, "import", errs)
	sw.requests()

	_, errs = p.plan("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{"authorized_keys": []string{keyA}})))
	expectError(t, "a plan dropping a key", errs,
		"user ops has authorized keys on the switch that nothing declares: ssh-rsa AAAAkeyB. A PATCH cannot remove one")

	_, errs = p.plan("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{"authorized_keys": []string{keyA, keyB + " laptop"}})))
	expectError(t, "a key with a comment", errs, `authorized key "ssh-rsa AAAAkeyB laptop": give its type and key material alone`)

	// A new group replaces the user, and the create it plans takes the
	// password, so the plan fails before the destroy.
	_, errs = p.apply("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{"password_wo": nil, "group": "operators"})))
	expectError(t, "a new group without password_wo", errs, "user ops: creating it takes password_wo")
	expectRequests(t, sw.writes())

	_, errs = p.apply("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{"password_wo": nil, "password_wo_version": 2})))
	expectError(t, "a new password_wo_version without password_wo", errs, "user ops: a changed password_wo_version takes password_wo")

	// The fake answers the refused PATCH with its body, the password in it;
	// the error must not repeat it.
	_, errs = p.apply("aoscx_user", state, p.config("aoscx_user", userConfig(map[string]interface{}{"password_wo_version": 2})))
	expectError(t, "a refused password", errs, "could not set user ops's password: PATCH "+opsUser+" answered HTTP 400")
	expectRequests(t, sw.writes(), "PATCH "+opsUser+` {"password": "user password 1"}`)
}

func TestUserReadRefusesAKeyNumberOutOfRange(t *testing.T) {
	for _, number := range []string{"0", "33", "01", "x"} {
		sw := newFakeSwitch(t)
		sw.member("/system/users", "ops", `{"name": "ops",
			"user_group": {"administrators": "/rest/v10.16/system/user_groups/administrators"},
			"authorized_keys": {"`+number+`": "ssh-ed25519 AAAAkeyA"}}`)
		p := configuredProvider(t, sw)
		_, errs := p.importState("aoscx_user", "ops")
		expectError(t, "import with key "+number, errs, `user ops has an authorized key numbered "`+number+`" that this cannot read`)
	}
}

func TestUserReadsTheManagementInterfacesTurnedOff(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.member("/system/users", "ops", `{"name": "ops",
		"user_group": {"administrators": "/rest/v10.16/system/user_groups/administrators"},
		"management_interface": {"ssh_enable": false, "rest_enable": true, "web_ui_enable": false}}`)
	p := configuredProvider(t, sw)
	imported, errs := p.importState("aoscx_user", "ops")
	expectOK(t, "import", errs)
	expectState(t, imported, `{"disabled_management_interfaces": ["ssh_enable", "web_ui_enable"], "authorized_keys": []}`)
}

// The read-back compares the keys as sets, so a switch that took the POST
// but holds a key fewer fails the create.
func TestUserFailsWhenTheSwitchDoesNotHoldItsKeys(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.afterWrite = func(method string, path string) {
		delete(sw.members["/system/users"]["ops"]["authorized_keys"].(map[string]interface{}), "2")
	}
	p := configuredProvider(t, sw)
	p.secret(userSecret)
	_, errs := p.create("aoscx_user", p.config("aoscx_user", userConfig(nil)))
	expectError(t, "create", errs, "the switch took the write, but its user ops has authorized_keys")
}
