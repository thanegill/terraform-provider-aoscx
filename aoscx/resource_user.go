package aoscx

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

const (
	users             = "/system/users"
	maxAuthorizedKeys = 32
)

func resourceUser() *schema.Resource {
	return &schema.Resource{
		Description: "A local user and the SSH keys it is authorized by (`user <name> group <group>`, " +
			"`user <name> authorized-key`), imported by its name. Keys are only ever added, each at the " +
			"lowest free key number, and a plan that would drop a key the switch has fails, since a PATCH " +
			"cannot remove one. A change of group replaces the user.",
		CreateContext: resourceUserCreate,
		ReadContext:   resourceUserRead,
		UpdateContext: resourceUserUpdate,
		DeleteContext: resourceUserDelete,
		CustomizeDiff: refuseUserKeyRemovals,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The user's name.",
			},
			"group": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The user's group, such as `administrators`.",
			},
			"authorized_keys": {
				Type:        schema.TypeSet,
				Optional:    true,
				MaxItems:    maxAuthorizedKeys,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "The SSH public keys the user logs in with, each its type and key material without a comment.",
			},
			"password_wo": {
				Type:        schema.TypeString,
				Optional:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The user's password, never stored in plan or state. Creating the user takes it, and it is sent again whenever password_wo_version changes; any other write leaves the password as it is.",
			},
			"password_wo_version": {
				Type:        schema.TypeInt,
				Optional:    true,
				Description: "Change this to send password_wo again.",
			},
			"disabled_management_interfaces": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "The management interfaces turned off for the user, such as `ssh_enable`.",
			},
		},
	}
}

func userPath(d *schema.ResourceData) string {
	return users + "/" + restKey(d.Id())
}

// keyIdentity is an SSH public key's type and key material, without its
// comment.
func keyIdentity(key string) string {
	fields := strings.Fields(key)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	return strings.Join(fields, " ")
}

// refuseUserKeyRemovals refuses a declared key with a comment, which would
// never match the key as read, a plan that drops a key the switch has, since
// a PATCH cannot remove one, and a create without a password.
func refuseUserKeyRemovals(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	have, want := d.GetChange("authorized_keys")
	for _, key := range want.(*schema.Set).List() {
		if keyIdentity(key.(string)) != key.(string) {
			return fmt.Errorf("authorized key %q: give its type and key material alone, without a comment", key)
		}
	}
	if d.Id() == "" {
		if d.GetRawConfig().GetAttr("password_wo").IsNull() {
			return fmt.Errorf("user %s: creating it takes password_wo; to keep a user that was removed "+
				"at the CLI without one, create it again there ('user %s group %s password') and apply again",
				d.Get("name"), d.Get("name"), d.Get("group"))
		}
		return nil
	}
	dropped := have.(*schema.Set).Difference(want.(*schema.Set)).List()
	if len(dropped) == 0 {
		return nil
	}
	keys := []string{}
	for _, key := range dropped {
		keys = append(keys, key.(string))
	}
	sort.Strings(keys)
	return fmt.Errorf("user %s has authorized keys on the switch that nothing declares: %s. A PATCH cannot "+
		"remove one, so refusing; remove each at the CLI with 'no user %s authorized-key <number>', after "+
		"checking it with 'show user %s authorized-key'", d.Id(), strings.Join(keys, ", "), d.Id(), d.Id())
}

// keysToAdd numbers each declared key the switch lacks, at the lowest free
// number.
func keysToAdd(declared []interface{}, have map[string]interface{}) (map[string]interface{}, error) {
	held := map[string]bool{}
	for _, key := range have {
		if text, ok := key.(string); ok {
			held[keyIdentity(text)] = true
		}
	}
	added := map[string]interface{}{}
	number := 1
	for _, key := range declared {
		if held[key.(string)] {
			continue
		}
		for ; number <= maxAuthorizedKeys; number++ {
			if _, taken := have[strconv.Itoa(number)]; !taken {
				break
			}
		}
		if number > maxAuthorizedKeys {
			return nil, fmt.Errorf("no free authorized key number left")
		}
		added[strconv.Itoa(number)] = key
		held[key.(string)] = true
		number++
	}
	return added, nil
}

func sortedKeys(d *schema.ResourceData) []interface{} {
	keys := d.Get("authorized_keys").(*schema.Set).List()
	sort.Slice(keys, func(i, j int) bool { return keys[i].(string) < keys[j].(string) })
	return keys
}

func resourceUserCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)
	name := d.Get("name").(string)
	group := d.Get("group").(string)
	keys, err := keysToAdd(sortedKeys(d), map[string]interface{}{})
	if err != nil {
		return diag.Errorf("user %s: %s", name, err)
	}
	body := map[string]interface{}{
		"name":            name,
		"user_group":      "/rest/" + sw.Version + "/system/user_groups/" + restKey(group),
		"authorized_keys": keys,
	}
	password, diags := writeOnlyString(d, "password_wo")
	if diags.HasError() {
		return diags
	}
	if password == "" {
		return diag.Errorf("user %s: creating it takes password_wo", name)
	}
	body["password"] = password
	if err := restWriteWithSecrets(sw, "POST", users, body, true); err != nil {
		return diag.Errorf("could not create user %s: %s", name, err)
	}
	d.SetId(name)
	return readBack(ctx, d, m, resourceUserRead, "user "+name, []string{"group", "authorized_keys"})
}

func resourceUserUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)
	if d.HasChange("authorized_keys") {
		attributes, err := restAttributes(sw, userPath(d), "authorized_keys")
		if err != nil {
			return diag.Errorf("could not read user %s: %s", d.Id(), err)
		}
		have, err := restMap(attributes, "authorized_keys")
		if err != nil {
			return diag.FromErr(err)
		}
		added, err := keysToAdd(sortedKeys(d), have)
		if err != nil {
			return diag.Errorf("user %s: %s", d.Id(), err)
		}
		if err := patchResource(sw, userPath(d), nil, map[string]map[string]interface{}{"authorized_keys": added}); err != nil {
			return diag.Errorf("could not set user %s's authorized keys: %s", d.Id(), err)
		}
	}
	if d.HasChange("password_wo_version") {
		password, diags := writeOnlyString(d, "password_wo")
		if diags.HasError() {
			return diags
		}
		if password == "" {
			return diag.Errorf("user %s: a changed password_wo_version takes password_wo", d.Id())
		}
		if err := restWriteWithSecrets(sw, "PATCH", userPath(d), map[string]interface{}{"password": password}, true); err != nil {
			return diag.Errorf("could not set user %s's password: %s", d.Id(), err)
		}
	}
	return resourceUserRead(ctx, d, m)
}

// resourceUserRead reads the user's group, its keys without their comments
// and the management interfaces turned off for it, and forgets a user the
// switch no longer has.
func resourceUserRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), userPath(d), "authorized_keys", "user_group", "management_interface")
	if isNotFound(err) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.Errorf("could not read user %s: %s", d.Id(), err)
	}
	groups, err := restMap(attributes, "user_group")
	if err != nil {
		return diag.FromErr(err)
	}
	if len(groups) != 1 {
		return diag.Errorf("user %s is in %d groups on the switch, not one", d.Id(), len(groups))
	}
	for group := range groups {
		d.Set("group", group)
	}
	keys, err := restMap(attributes, "authorized_keys")
	if err != nil {
		return diag.FromErr(err)
	}
	identities := []interface{}{}
	for number, key := range keys {
		parsed, err := strconv.Atoi(number)
		text, ok := key.(string)
		if err != nil || strconv.Itoa(parsed) != number || parsed < 1 || parsed > maxAuthorizedKeys || !ok {
			return diag.Errorf("user %s has an authorized key numbered %q that this cannot read", d.Id(), number)
		}
		identities = append(identities, keyIdentity(text))
	}
	interfaces, err := restMap(attributes, "management_interface")
	if err != nil {
		return diag.FromErr(err)
	}
	disabled := []string{}
	for name := range interfaces {
		enabled, err := configBool(interfaces, name, true)
		if err != nil {
			return diag.FromErr(err)
		}
		if !enabled {
			disabled = append(disabled, name)
		}
	}
	sort.Strings(disabled)
	d.Set("name", d.Id())
	d.Set("authorized_keys", identities)
	d.Set("disabled_management_interfaces", disabled)
	return nil
}

func resourceUserDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if err := restWrite(m.(*aoscxgo.Client), "DELETE", userPath(d), nil); err != nil && !isNotFound(err) {
		return diag.Errorf("could not delete user %s: %s", d.Id(), err)
	}
	d.SetId("")
	return nil
}
