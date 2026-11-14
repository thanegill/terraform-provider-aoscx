package aoscx

import (
	"context"
	"fmt"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

const defaultPortMtu = 1500

func resourcePortSettings() *schema.Resource {
	return &schema.Resource{
		Description: "A physical port's MTU and admin state (`mtu`, `shutdown`), kept in its user_config " +
			"beside its speeds, flow control and the rest. A port always exists, so create sets its MTU and " +
			"admin state rather than creating it, and delete only forgets it; import it by the port's name.",
		CreateContext: resourcePortSettingsCreate,
		ReadContext:   resourcePortSettingsRead,
		UpdateContext: resourcePortSettingsUpdate,
		DeleteContext: forget,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"interface": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The port's name, such as `1/1/14`.",
			},
			"mtu": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(46, 9198),
				Description:  "The port's MTU.",
			},
			"admin_state": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice([]string{"up", "down"}, false),
				Description:  "`up`, or `down` for a port shut down.",
			},
		},
	}
}

func portPath(name string) string {
	return "/system/interfaces/" + restKey(name)
}

// writePortSettings sets the changed settings in user_config, sent whole: the
// switch's map with these keys set.
func writePortSettings(d *schema.ResourceData, m interface{}, name string, all bool) diag.Diagnostics {
	keys := map[string]interface{}{}
	if all || d.HasChange("mtu") {
		keys["mtu"] = d.Get("mtu").(int)
	}
	if all || d.HasChange("admin_state") {
		keys["admin"] = d.Get("admin_state").(string)
	}
	maps := map[string]map[string]interface{}{"user_config": keys}
	if err := patchResource(m.(*aoscxgo.Client), portPath(name), nil, maps); err != nil {
		return diag.Errorf("could not set port %s's MTU and admin state: %s", name, err)
	}
	return nil
}

func resourcePortSettingsCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	name := d.Get("interface").(string)
	if diags := writePortSettings(d, m, name, true); diags.HasError() {
		return diags
	}
	d.SetId(name)
	return resourcePortSettingsRead(ctx, d, m)
}

func resourcePortSettingsUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writePortSettings(d, m, d.Id(), false); diags.HasError() {
		return diags
	}
	return resourcePortSettingsRead(ctx, d, m)
}

// resourcePortSettingsRead reads the MTU, 1500 when unset, and the admin
// state, up when unset. A port always exists, so a failed read is an error,
// never a reason to forget it.
func resourcePortSettingsRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), portPath(d.Id()), "user_config")
	if err != nil {
		return diag.Errorf("could not read port %s: %s", d.Id(), err)
	}
	config, err := restMap(attributes, "user_config")
	if err != nil {
		return diag.FromErr(err)
	}
	mtu, err := configInt(config, "mtu", defaultPortMtu)
	if err != nil {
		return diag.Errorf("port %s: %s", d.Id(), err)
	}
	admin, err := configString(config, "admin", "up")
	if err != nil {
		return diag.Errorf("port %s: %s", d.Id(), err)
	}
	if admin != "up" && admin != "down" {
		return diag.FromErr(fmt.Errorf("port %s: unknown admin state %q", d.Id(), admin))
	}
	d.Set("interface", d.Id())
	d.Set("mtu", mtu)
	d.Set("admin_state", admin)
	return nil
}
