package aoscx

import (
	"context"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

const ntpAssociations = "/system/vrfs/default/ntp_associations"

var ntpManaged = []string{"burst_mode", "minpoll", "maxpoll", "ntp_version", "prefer"}

func resourceNtpServer() *schema.Resource {
	return &schema.Resource{
		Description: "An NTP server of the default VRF (`ntp server <address> ...`), " +
			"imported by its address. Its association attributes are managed and its " +
			"authentication key is left as it is.",
		CreateContext: resourceNtpServerCreate,
		ReadContext:   resourceNtpServerRead,
		UpdateContext: resourceNtpServerUpdate,
		DeleteContext: resourceNtpServerDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"address": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The server's address or host name.",
			},
			"burst_mode": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice([]string{"none", "burst", "iburst"}, false),
				Description:  "`burst`, `iburst` or `none`.",
			},
			"minpoll": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(4, 17),
				Description:  "The shortest poll interval, as a power of 2 seconds.",
			},
			"maxpoll": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(4, 17),
				Description:  "The longest poll interval, as a power of 2 seconds.",
			},
			"ntp_version": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(3, 4),
				Description:  "The NTP version spoken to the server.",
			},
			"prefer": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether the server is preferred over the others.",
			},
		},
	}
}

func ntpServerPath(d *schema.ResourceData) string {
	return ntpAssociations + "/" + restKey(d.Id())
}

func ntpAttributes(d *schema.ResourceData, all bool) map[string]interface{} {
	attributes := map[string]interface{}{}
	for _, name := range ntpManaged {
		if all || d.HasChange(name) {
			attributes[name] = d.Get(name)
		}
	}
	return attributes
}

func resourceNtpServerCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	address := d.Get("address").(string)
	body := map[string]interface{}{
		"address":                address,
		"association_attributes": ntpAttributes(d, true),
	}
	if err := restWrite(m.(*aoscxgo.Client), "POST", ntpAssociations, body); err != nil {
		return diag.Errorf("could not create NTP server %s: %s", address, err)
	}
	d.SetId(address)
	return readBack(ctx, d, m, resourceNtpServerRead, "NTP server "+address, ntpManaged)
}

// resourceNtpServerUpdate sends association_attributes whole, the switch's
// with the changed attributes set, since they share one map.
func resourceNtpServerUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	maps := map[string]map[string]interface{}{"association_attributes": ntpAttributes(d, false)}
	if err := patchMember(m.(*aoscxgo.Client), ntpServerPath(d), nil, maps); err != nil {
		return diag.Errorf("could not set NTP server %s: %s", d.Id(), err)
	}
	return resourceNtpServerRead(ctx, d, m)
}

// resourceNtpServerRead reads the server's association attributes, each one
// the switch lacks as unset, and forgets a server the switch no longer has.
func resourceNtpServerRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes := map[string]interface{}{}
	err := restGet(m.(*aoscxgo.Client), ntpServerPath(d)+"?selector=writable", &attributes)
	if isNotFound(err) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.Errorf("could not read NTP server %s: %s", d.Id(), err)
	}
	association, err := restMap(attributes, "association_attributes")
	if err != nil {
		return diag.FromErr(err)
	}
	burstMode, err := configString(association, "burst_mode", "")
	if err != nil {
		return diag.FromErr(err)
	}
	prefer, err := configBool(association, "prefer", false)
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("address", d.Id())
	d.Set("burst_mode", burstMode)
	d.Set("prefer", prefer)
	for _, name := range []string{"minpoll", "maxpoll", "ntp_version"} {
		value, err := configInt(association, name, 0)
		if err != nil {
			return diag.FromErr(err)
		}
		d.Set(name, value)
	}
	return nil
}

func resourceNtpServerDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if err := restWrite(m.(*aoscxgo.Client), "DELETE", ntpServerPath(d), nil); err != nil && !isNotFound(err) {
		return diag.Errorf("could not delete NTP server %s: %s", d.Id(), err)
	}
	d.SetId("")
	return nil
}
