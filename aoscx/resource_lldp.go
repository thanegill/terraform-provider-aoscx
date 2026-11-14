package aoscx

import (
	"context"
	"fmt"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// The TLVs LLDP may advertise, each turned on and off by the other_config key
// lldp_tlv_<name>_enable, which is on when unset.
var lldpTlvs = []string{
	"dot1_link_aggregation",
	"mgmt_addr",
	"org_specific",
	"port_desc",
	"port_vlan",
	"port_vlan_name",
	"sys_cap",
	"sys_desc",
	"sys_name",
}

// An LLDP integer setting's key in other_config, and its value when unset.
type lldpInteger struct {
	key   string
	unset int
}

var lldpIntegers = map[string]lldpInteger{
	"tx_interval":     {"lldp_tx_interval", 30},
	"hold_multiplier": {"lldp_hold", 4},
	"tx_delay":        {"lldp_txdelay", 2},
	"reinit_delay":    {"lldp_reinit", 2},
}

func resourceLldp() *schema.Resource {
	integer := func(description string, unset int) *schema.Schema {
		return &schema.Schema{
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      unset,
			ValidateFunc: validation.IntAtLeast(0),
			Description:  description,
		}
	}
	return &schema.Resource{
		Description: "LLDP's global settings (`lldp`, `lldp timer`, `lldp holdtime-multiplier`, `lldp txdelay`, " +
			"`lldp reinit`, `lldp trap enable`, `lldp select-tlv`), kept in /system's other_config. " +
			"The ports' own LLDP settings are left as they are. The switch has one, so create adopts it and " +
			"delete only forgets it; import it by the id `system`.",
		CreateContext: resourceLldpCreate,
		ReadContext:   resourceLldpRead,
		UpdateContext: resourceLldpUpdate,
		DeleteContext: forget,
		Importer:      importSingleton("system"),
		Schema: map[string]*schema.Schema{
			"enable": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether LLDP runs.",
			},
			"tx_interval":     integer("Seconds between advertisements.", lldpIntegers["tx_interval"].unset),
			"hold_multiplier": integer("The hold time as a multiple of tx_interval.", lldpIntegers["hold_multiplier"].unset),
			"tx_delay":        integer("Seconds between two advertisements a change triggers.", lldpIntegers["tx_delay"].unset),
			"reinit_delay":    integer("Seconds before LLDP starts again on a port it stopped on.", lldpIntegers["reinit_delay"].unset),
			"trap_enable": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether LLDP sends SNMP traps.",
			},
			"advertise": {
				Type:     schema.TypeSet,
				Required: true,
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: validation.StringInSlice(lldpTlvs, false),
				},
				Description: fmt.Sprintf("The TLVs advertised; every other one is turned off. One of %v.", lldpTlvs),
			},
		},
	}
}

func writeLldp(d *schema.ResourceData, m interface{}, all bool) diag.Diagnostics {
	keys := map[string]interface{}{}
	if all || d.HasChange("enable") {
		keys["lldp_disable"] = !d.Get("enable").(bool)
	}
	for attribute, setting := range lldpIntegers {
		if all || d.HasChange(attribute) {
			keys[setting.key] = d.Get(attribute).(int)
		}
	}
	if all || d.HasChange("trap_enable") {
		keys["lldp_trap_enable"] = d.Get("trap_enable").(bool)
	}
	if all || d.HasChange("advertise") {
		advertised := d.Get("advertise").(*schema.Set)
		for _, tlv := range lldpTlvs {
			keys["lldp_tlv_"+tlv+"_enable"] = advertised.Contains(tlv)
		}
	}
	if err := patchResource(m.(*aoscxgo.Client), "/system", nil, map[string]map[string]interface{}{"other_config": keys}); err != nil {
		return diag.Errorf("could not set LLDP: %s", err)
	}
	return nil
}

func resourceLldpCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeLldp(d, m, true); diags.HasError() {
		return diags
	}
	d.SetId("system")
	return resourceLldpRead(ctx, d, m)
}

func resourceLldpUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeLldp(d, m, false); diags.HasError() {
		return diags
	}
	return resourceLldpRead(ctx, d, m)
}

// resourceLldpRead reads LLDP's settings, each unset one as the switch
// defaults it.
func resourceLldpRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), "/system", "other_config")
	if err != nil {
		return diag.Errorf("could not read LLDP: %s", err)
	}
	config, err := restMap(attributes, "other_config")
	if err != nil {
		return diag.FromErr(err)
	}
	disabled, err := configBool(config, "lldp_disable", false)
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("enable", !disabled)
	for attribute, setting := range lldpIntegers {
		value, err := configInt(config, setting.key, setting.unset)
		if err != nil {
			return diag.FromErr(err)
		}
		d.Set(attribute, value)
	}
	trap, err := configBool(config, "lldp_trap_enable", false)
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("trap_enable", trap)
	advertised := []interface{}{}
	for _, tlv := range lldpTlvs {
		enabled, err := configBool(config, "lldp_tlv_"+tlv+"_enable", true)
		if err != nil {
			return diag.FromErr(err)
		}
		if enabled {
			advertised = append(advertised, tlv)
		}
	}
	d.Set("advertise", advertised)
	return nil
}
