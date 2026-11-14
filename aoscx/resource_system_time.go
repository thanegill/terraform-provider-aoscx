package aoscx

import (
	"context"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceSystemTime() *schema.Resource {
	return &schema.Resource{
		Description: "The time zone and whether NTP is on (`clock timezone`, `ntp enable`). " +
			"The switch has one, so create adopts it and delete only forgets it; import it by the id `system`.",
		CreateContext: resourceSystemTimeCreate,
		ReadContext:   resourceSystemTimeRead,
		UpdateContext: resourceSystemTimeUpdate,
		DeleteContext: forget,
		Importer:      importSingleton("system"),
		Schema: map[string]*schema.Schema{
			"timezone": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The time zone, as the switch names it, such as `America/Los_Angeles`.",
			},
			"ntp_enable": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether the switch sets its clock by NTP.",
			},
		},
	}
}

func writeSystemTime(d *schema.ResourceData, m interface{}, all bool) diag.Diagnostics {
	values := map[string]interface{}{}
	maps := map[string]map[string]interface{}{}
	if all || d.HasChange("timezone") {
		values["timezone"] = d.Get("timezone").(string)
	}
	if all || d.HasChange("ntp_enable") {
		maps["ntp_config"] = map[string]interface{}{"enable": d.Get("ntp_enable").(bool)}
	}
	if err := patchResource(m.(*aoscxgo.Client), "/system", values, maps); err != nil {
		return diag.Errorf("could not set the time zone and NTP: %s", err)
	}
	return nil
}

func resourceSystemTimeCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeSystemTime(d, m, true); diags.HasError() {
		return diags
	}
	d.SetId("system")
	return resourceSystemTimeRead(ctx, d, m)
}

func resourceSystemTimeUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeSystemTime(d, m, false); diags.HasError() {
		return diags
	}
	return resourceSystemTimeRead(ctx, d, m)
}

// resourceSystemTimeRead reads the time zone, UTC when unset, and whether NTP
// is on, off when unset.
func resourceSystemTimeRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), "/system", "timezone", "ntp_config")
	if err != nil {
		return diag.Errorf("could not read the time zone and NTP: %s", err)
	}
	timezone, err := configString(attributes, "timezone", "UTC")
	if err != nil {
		return diag.FromErr(err)
	}
	ntpConfig, err := restMap(attributes, "ntp_config")
	if err != nil {
		return diag.FromErr(err)
	}
	enable, err := configBool(ntpConfig, "enable", false)
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("timezone", timezone)
	d.Set("ntp_enable", enable)
	return nil
}
