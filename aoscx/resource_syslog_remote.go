package aoscx

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

const syslogRemotes = "/system/syslog_remotes"

// Each transport's port when none is set.
var syslogPorts = map[string]int{"udp": 514, "tcp": 1470, "tls": 6514}

var syslogManaged = []string{"transport", "port_number", "severity", "vrf", "include_auditable_events", "disable"}

var syslogSeverities = []string{"debug", "info", "notice", "warning", "err", "crit", "alert", "emerg"}

func resourceSyslogRemote() *schema.Resource {
	return &schema.Resource{
		Description: "A remote syslog server (`logging <address> ...`), imported by its address. " +
			"Its transport, port, severity, VRF, audit events and whether it is disabled are managed; " +
			"its filter, rate limit and TLS settings are left as they are.",
		CreateContext: resourceSyslogRemoteCreate,
		ReadContext:   resourceSyslogRemoteRead,
		UpdateContext: resourceSyslogRemoteUpdate,
		DeleteContext: resourceSyslogRemoteDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"remote_host": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The server's address or host name.",
			},
			"transport": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice([]string{"udp", "tcp", "tls"}, false),
				Description:  "`udp`, `tcp` or `tls`.",
			},
			"port_number": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "The server's port.",
			},
			"severity": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice(syslogSeverities, false),
				Description:  "The least severe level sent.",
			},
			"vrf": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The VRF the server is reached through.",
			},
			"include_auditable_events": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether audit events are sent too.",
			},
			"disable": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether sending to the server is turned off.",
			},
		},
	}
}

func syslogRemotePath(d *schema.ResourceData) string {
	return syslogRemotes + "/" + restKey(d.Id())
}

// syslogSettings are the managed settings to write, the VRF as the reference
// the switch takes: all of them, or the changed ones, with the port whenever
// the transport changes, since the switch defaults the port from it.
func syslogSettings(d *schema.ResourceData, sw *aoscxgo.Client, all bool) map[string]interface{} {
	settings := map[string]interface{}{}
	for _, name := range syslogManaged {
		if all || d.HasChange(name) || (name == "port_number" && d.HasChange("transport")) {
			settings[name] = d.Get(name)
		}
	}
	if vrf, ok := settings["vrf"]; ok {
		settings["vrf"] = "/rest/" + sw.Version + vrfPath(vrf.(string))
	}
	return settings
}

func resourceSyslogRemoteCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)
	host := d.Get("remote_host").(string)
	body := syslogSettings(d, sw, true)
	body["remote_host"] = host
	if err := restWrite(sw, "POST", syslogRemotes, body); err != nil {
		return diag.Errorf("could not create syslog remote %s: %s", host, err)
	}
	d.SetId(host)
	return readBack(ctx, d, m, resourceSyslogRemoteRead, "syslog remote "+d.Id(), syslogManaged)
}

func resourceSyslogRemoteUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)
	if err := restWrite(sw, "PATCH", syslogRemotePath(d), syslogSettings(d, sw, false)); err != nil {
		return diag.Errorf("could not set syslog remote %s: %s", d.Id(), err)
	}
	return readBack(ctx, d, m, resourceSyslogRemoteRead, "syslog remote "+d.Id(), syslogManaged)
}

// vrfName is the VRF a reference names, as the switch answers one: a path, or
// a map of the VRF's name to its path. None is the default VRF.
func vrfName(reference interface{}) (string, error) {
	switch value := reference.(type) {
	case nil:
		return "default", nil
	case string:
		prefix, name, found := strings.Cut(value, "/system/vrfs/")
		if found && prefix != "" && name != "" {
			if unescaped, err := url.PathUnescape(name); err == nil {
				return unescaped, nil
			}
		}
	case map[string]interface{}:
		if len(value) == 1 {
			for name := range value {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("a VRF this cannot read, %#v", reference)
}

// resourceSyslogRemoteRead reads the managed settings, each unset one as the
// switch defaults it, and forgets a server the switch no longer has.
func resourceSyslogRemoteRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	entry := map[string]interface{}{}
	err := restGet(m.(*aoscxgo.Client), syslogRemotePath(d)+"?selector=writable", &entry)
	if isNotFound(err) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.Errorf("could not read syslog remote %s: %s", d.Id(), err)
	}
	label := "syslog remote " + d.Id()
	transport, err := configString(entry, "transport", "udp")
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	unset, known := syslogPorts[transport]
	if !known {
		return diag.Errorf("%s: unknown transport %q", label, transport)
	}
	port, err := configInt(entry, "port_number", unset)
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	severity, err := configString(entry, "severity", "info")
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	known = false
	for _, level := range syslogSeverities {
		known = known || level == severity
	}
	if !known {
		return diag.Errorf("%s: unknown severity %q", label, severity)
	}
	vrf, err := vrfName(entry["vrf"])
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	audit, err := configBool(entry, "include_auditable_events", false)
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	disable, err := configBool(entry, "disable", false)
	if err != nil {
		return diag.Errorf("%s: %s", label, err)
	}
	d.Set("remote_host", d.Id())
	d.Set("transport", transport)
	d.Set("port_number", port)
	d.Set("severity", severity)
	d.Set("vrf", vrf)
	d.Set("include_auditable_events", audit)
	d.Set("disable", disable)
	return nil
}

func resourceSyslogRemoteDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	if err := restWrite(m.(*aoscxgo.Client), "DELETE", syslogRemotePath(d), nil); err != nil && !isNotFound(err) {
		return diag.Errorf("could not delete syslog remote %s: %s", d.Id(), err)
	}
	d.SetId("")
	return nil
}
