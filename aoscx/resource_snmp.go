package aoscx

import (
	"context"
	"fmt"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

const snmpv3Users = "/system/snmpv3_users"

var snmpv3UserManaged = []string{"access_level", "auth_protocol", "priv_protocol"}

func resourceSystemSnmp() *schema.Resource {
	return &schema.Resource{
		Description: "Whether the switch answers SNMPv3 alone (`snmp-server snmpv3-only`), which leaves " +
			"the SNMPv1 and v2c communities unanswered. The switch has one, so create adopts it and " +
			"delete only forgets it; import it by the id `system`.",
		CreateContext: resourceSystemSnmpCreate,
		ReadContext:   resourceSystemSnmpRead,
		UpdateContext: resourceSystemSnmpUpdate,
		DeleteContext: forget,
		Importer:      importSingleton("system"),
		Schema: map[string]*schema.Schema{
			"snmpv3_only": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether only SNMPv3 is answered.",
			},
		},
	}
}

func writeSystemSnmp(d *schema.ResourceData, m interface{}) diag.Diagnostics {
	values := map[string]interface{}{"enable_snmpv3_only": d.Get("snmpv3_only").(bool)}
	if err := patchResource(m.(*aoscxgo.Client), "/system", values, nil); err != nil {
		return diag.Errorf("could not set SNMPv3-only: %s", err)
	}
	return nil
}

func resourceSystemSnmpCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeSystemSnmp(d, m); diags.HasError() {
		return diags
	}
	d.SetId("system")
	return resourceSystemSnmpRead(ctx, d, m)
}

func resourceSystemSnmpUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeSystemSnmp(d, m); diags.HasError() {
		return diags
	}
	return resourceSystemSnmpRead(ctx, d, m)
}

func resourceSystemSnmpRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), "/system", "enable_snmpv3_only")
	if err != nil {
		return diag.Errorf("could not read SNMPv3-only: %s", err)
	}
	only, err := configBool(attributes, "enable_snmpv3_only", false)
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("snmpv3_only", only)
	return nil
}

func resourceSnmpv3User() *schema.Resource {
	return &schema.Resource{
		Description: "An SNMPv3 user (`snmpv3 user <name> auth ... priv ...`), imported by its name. " +
			"The switch keeps the pass phrases encrypted and derives its keys from each protocol and pass " +
			"phrase together, so the pass phrases are write-only and sent only when the user is created, " +
			"and a change of either protocol or of pass_phrase_wo_version replaces the user.",
		CreateContext: resourceSnmpv3UserCreate,
		ReadContext:   resourceSnmpv3UserRead,
		UpdateContext: resourceSnmpv3UserUpdate,
		DeleteContext: resourceSnmpv3UserDelete,
		CustomizeDiff: refuseSnmpv3UserWithoutPassPhrases,
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
			"access_level": {
				Type:         schema.TypeString,
				Optional:     true,
				Default:      "ro",
				ValidateFunc: validation.StringInSlice([]string{"ro", "rw"}, false),
				Description:  "`ro` or `rw`.",
			},
			"auth_protocol": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The authentication protocol, such as `sha256`.",
			},
			"priv_protocol": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The privacy protocol, such as `aes`.",
			},
			"auth_pass_phrase_wo": {
				Type:        schema.TypeString,
				Optional:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The authentication pass phrase, never stored in plan or state; sent only when the user is created.",
			},
			"priv_pass_phrase_wo": {
				Type:        schema.TypeString,
				Optional:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The privacy pass phrase, never stored in plan or state; sent only when the user is created.",
			},
			"pass_phrase_wo_version": {
				Type:        schema.TypeInt,
				Optional:    true,
				ForceNew:    true,
				Description: "Change this to create the user again with the pass phrases.",
			},
		},
	}
}

// refuseSnmpv3UserWithoutPassPhrases refuses a plan that creates the user
// without both pass phrases, a replacement's create included, which would
// otherwise fail only after the replacement deleted the user.
func refuseSnmpv3UserWithoutPassPhrases(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	if d.Id() != "" {
		return nil
	}
	for _, attribute := range []string{"auth_pass_phrase_wo", "priv_pass_phrase_wo"} {
		if d.GetRawConfig().GetAttr(attribute).IsNull() {
			return fmt.Errorf("SNMPv3 user %s: creating it takes %s", d.Get("name"), attribute)
		}
	}
	return nil
}

func snmpv3UserPath(d *schema.ResourceData) string {
	return snmpv3Users + "/" + restKey(d.Id())
}

// writeOnlyString is a write-only attribute's value in the configuration,
// empty when it is unset.
func writeOnlyString(d *schema.ResourceData, name string) (string, diag.Diagnostics) {
	value, diags := d.GetRawConfigAt(cty.GetAttrPath(name))
	if diags.HasError() || value.IsNull() || !value.IsKnown() {
		return "", diags
	}
	return value.AsString(), diags
}

func resourceSnmpv3UserCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	name := d.Get("name").(string)
	body := map[string]interface{}{
		"user_name":     name,
		"access_level":  d.Get("access_level").(string),
		"auth_protocol": d.Get("auth_protocol").(string),
		"priv_protocol": d.Get("priv_protocol").(string),
	}
	for attribute, key := range map[string]string{
		"auth_pass_phrase_wo": "auth_pass_phrase",
		"priv_pass_phrase_wo": "priv_pass_phrase",
	} {
		passPhrase, diags := writeOnlyString(d, attribute)
		if diags.HasError() {
			return diags
		}
		if passPhrase == "" {
			return diag.Errorf("SNMPv3 user %s: creating it takes %s", name, attribute)
		}
		body[key] = passPhrase
	}
	if err := restWriteWithSecrets(m.(*aoscxgo.Client), "POST", snmpv3Users, body, true); err != nil {
		return diag.Errorf("could not create SNMPv3 user %s: %s", name, err)
	}
	d.SetId(name)
	return readBack(ctx, d, m, resourceSnmpv3UserRead, "SNMPv3 user "+name, snmpv3UserManaged)
}

// resourceSnmpv3UserUpdate sets the access level, the one attribute a
// change of which does not replace the user.
func resourceSnmpv3UserUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if d.HasChange("access_level") {
		values := map[string]interface{}{"access_level": d.Get("access_level").(string)}
		if err := patchMember(m.(*aoscxgo.Client), snmpv3UserPath(d), values, nil); err != nil {
			return diag.Errorf("could not set SNMPv3 user %s: %s", d.Id(), err)
		}
	}
	return resourceSnmpv3UserRead(ctx, d, m)
}

// resourceSnmpv3UserRead reads the access level and protocols, each unset one
// as the switch defaults it, and forgets a user the switch no longer has. The
// switch's encrypted pass phrases are never read.
func resourceSnmpv3UserRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	entry := map[string]interface{}{}
	err := restGet(m.(*aoscxgo.Client), snmpv3UserPath(d)+"?attributes=access_level,auth_protocol,priv_protocol", &entry)
	if isNotFound(err) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.Errorf("could not read SNMPv3 user %s: %s", d.Id(), err)
	}
	for attribute, unset := range map[string]string{
		"access_level":  "ro",
		"auth_protocol": "none",
		"priv_protocol": "none",
	} {
		value, err := configString(entry, attribute, unset)
		if err != nil {
			return diag.FromErr(err)
		}
		d.Set(attribute, value)
	}
	d.Set("name", d.Id())
	return nil
}

func resourceSnmpv3UserDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if err := restWrite(m.(*aoscxgo.Client), "DELETE", snmpv3UserPath(d), nil); err != nil && !isNotFound(err) {
		return diag.Errorf("could not delete SNMPv3 user %s: %s", d.Id(), err)
	}
	d.SetId("")
	return nil
}
