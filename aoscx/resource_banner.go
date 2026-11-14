package aoscx

import (
	"context"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Each banner attribute's key in /system's other_config.
var bannerKeys = map[string]string{
	"motd": "banner",
	"exec": "banner_exec",
}

func resourceBanner() *schema.Resource {
	return &schema.Resource{
		Description: "The banners shown before and after login (`banner motd`, `banner exec`). " +
			"The switch has one, so create adopts it and delete only forgets it; import it by the id `system`.",
		CreateContext: resourceBannerCreate,
		ReadContext:   resourceBannerRead,
		UpdateContext: resourceBannerUpdate,
		DeleteContext: forget,
		Importer:      importSingleton("system"),
		Schema: map[string]*schema.Schema{
			"motd": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The banner before login; empty for none.",
			},
			"exec": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The banner after login; empty for none.",
			},
		},
	}
}

func writeBanner(d *schema.ResourceData, m interface{}, all bool) diag.Diagnostics {
	keys := map[string]interface{}{}
	for attribute, key := range bannerKeys {
		if all || d.HasChange(attribute) {
			keys[key] = d.Get(attribute).(string)
		}
	}
	if err := patchResource(m.(*aoscxgo.Client), "/system", nil, map[string]map[string]interface{}{"other_config": keys}); err != nil {
		return diag.Errorf("could not set the banners: %s", err)
	}
	return nil
}

func resourceBannerCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeBanner(d, m, true); diags.HasError() {
		return diags
	}
	d.SetId("system")
	return resourceBannerRead(ctx, d, m)
}

func resourceBannerUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeBanner(d, m, false); diags.HasError() {
		return diags
	}
	return resourceBannerRead(ctx, d, m)
}

// resourceBannerRead reads each banner, empty when the switch has none.
func resourceBannerRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), "/system", "other_config")
	if err != nil {
		return diag.Errorf("could not read the banners: %s", err)
	}
	config, err := restMap(attributes, "other_config")
	if err != nil {
		return diag.FromErr(err)
	}
	for attribute, key := range bannerKeys {
		banner, err := configString(config, key, "")
		if err != nil {
			return diag.FromErr(err)
		}
		d.Set(attribute, banner)
	}
	return nil
}
