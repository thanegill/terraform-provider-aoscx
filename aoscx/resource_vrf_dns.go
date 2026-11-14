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

func resourceVrfDns() *schema.Resource {
	return &schema.Resource{
		Description: "A VRF's DNS client: its name servers and domain (`ip dns server-address ... vrf <vrf>`, " +
			"`ip dns domain-name ... vrf <vrf>`). The VRF must exist; create adopts its DNS client and delete " +
			"only forgets it. Import it by the VRF's name.",
		CreateContext: resourceVrfDnsCreate,
		ReadContext:   resourceVrfDnsRead,
		UpdateContext: resourceVrfDnsUpdate,
		DeleteContext: forget,
		CustomizeDiff: refuseVrfDnsRemovals,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"vrf": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The VRF's name.",
			},
			"name_servers": {
				Type:        schema.TypeList,
				Required:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "The name servers' addresses, by priority from 0 in this order.",
			},
			"domain_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The domain appended to an unqualified name.",
			},
		},
	}
}

func vrfPath(vrf string) string {
	return "/system/vrfs/" + restKey(vrf)
}

// refuseVrfDnsRemovals refuses a plan that drops a name server the switch
// has: a PATCH cannot remove one from dns_name_servers.
func refuseVrfDnsRemovals(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	if d.Id() == "" {
		return nil
	}
	have, want := d.GetChange("name_servers")
	haveServers, wantServers := have.([]interface{}), want.([]interface{})
	if len(haveServers) <= len(wantServers) {
		return nil
	}
	extra := []string{}
	for _, server := range haveServers[len(wantServers):] {
		extra = append(extra, server.(string))
	}
	return fmt.Errorf("the switch's DNS client of VRF %s has name servers at priorities nothing declares: %s. "+
		"A PATCH cannot remove one, so refusing; remove each at the CLI with "+
		"'no ip dns server-address <address> vrf %s'", d.Id(), strings.Join(extra, ", "), d.Id())
}

func writeVrfDns(d *schema.ResourceData, m interface{}, vrf string, all bool) diag.Diagnostics {
	values := map[string]interface{}{}
	maps := map[string]map[string]interface{}{}
	if all || d.HasChange("name_servers") {
		servers := map[string]interface{}{}
		for priority, server := range d.Get("name_servers").([]interface{}) {
			servers[strconv.Itoa(priority)] = server
		}
		maps["dns_name_servers"] = servers
	}
	if all || d.HasChange("domain_name") {
		values["dns_domain_name"] = d.Get("domain_name").(string)
	}
	if err := patchResource(m.(*aoscxgo.Client), vrfPath(vrf), values, maps); err != nil {
		return diag.Errorf("could not set the DNS client of VRF %s: %s", vrf, err)
	}
	return nil
}

func resourceVrfDnsCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	vrf := d.Get("vrf").(string)
	if diags := writeVrfDns(d, m, vrf, true); diags.HasError() {
		return diags
	}
	d.SetId(vrf)
	return resourceVrfDnsRead(ctx, d, m)
}

func resourceVrfDnsUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if diags := writeVrfDns(d, m, d.Id(), false); diags.HasError() {
		return diags
	}
	return resourceVrfDnsRead(ctx, d, m)
}

// resourceVrfDnsRead reads the name servers in the order of their priority.
// A VRF is never forgotten: one the switch lacks is an error.
func resourceVrfDnsRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	attributes, err := restAttributes(m.(*aoscxgo.Client), vrfPath(d.Id()), "dns_name_servers", "dns_domain_name")
	if err != nil {
		return diag.Errorf("could not read the DNS client of VRF %s: %s", d.Id(), err)
	}
	servers, err := restMap(attributes, "dns_name_servers")
	if err != nil {
		return diag.FromErr(err)
	}
	priorities := make([]int, 0, len(servers))
	for key := range servers {
		priority, err := strconv.Atoi(key)
		if err != nil {
			return diag.Errorf("VRF %s has a name server at the priority %q, not a number", d.Id(), key)
		}
		priorities = append(priorities, priority)
	}
	sort.Ints(priorities)
	ordered := make([]interface{}, 0, len(priorities))
	for index, priority := range priorities {
		server, ok := servers[strconv.Itoa(priority)].(string)
		if !ok {
			return diag.Errorf("VRF %s has a name server at priority %d that is not an address", d.Id(), priority)
		}
		// A gap in the priorities would read as the list without it, and the
		// PATCH that writes the list could not remove the server above it.
		if priority != index {
			return diag.Errorf("the switch's DNS client of VRF %s has name server %s at priority %d, with "+
				"none at %d; this takes priorities from 0 without gaps, so refusing. Remove it at the CLI "+
				"with 'no ip dns server-address %s vrf %s'", d.Id(), server, priority, index, server, d.Id())
		}
		ordered = append(ordered, server)
	}
	domain, err := configString(attributes, "dns_domain_name", "")
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("vrf", d.Id())
	d.Set("name_servers", ordered)
	d.Set("domain_name", domain)
	return nil
}
