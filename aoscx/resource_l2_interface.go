package aoscx

import (
	"context"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// The port's description and admin state belong to aoscx_interface, so this
// resource has neither: it sets the port's VLANs and nothing else.
func resourceL2Interface() *schema.Resource {
	return &schema.Resource{
		Description:   "Resource to configure interface Layer2 attributes on AOS-CX switches.",
		CreateContext: resourceL2InterfaceCreate,
		ReadContext:   resourceL2InterfaceRead,
		UpdateContext: resourceL2InterfaceUpdate,
		DeleteContext: resourceL2InterfaceDelete,

		Schema: map[string]*schema.Schema{
			"interface": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"vlan_mode": &schema.Schema{
				Type:         schema.TypeString,
				Required:     false,
				Default:      "access",
				Optional:     true,
				ValidateFunc: validation.StringInSlice([]string{"access", "trunk"}, false),
			},
			"vlan_tag": &schema.Schema{
				Type:         schema.TypeInt,
				Required:     false,
				Default:      1,
				Optional:     true,
				ValidateFunc: validation.IntBetween(1, 4094),
			},
			"vlan_ids": &schema.Schema{
				Type:     schema.TypeSet,
				Required: false,
				Elem: &schema.Schema{
					Type:         schema.TypeInt,
					ValidateFunc: validation.IntBetween(1, 4094),
				},
				Optional: true,
			},
			"trunk_allowed_all": &schema.Schema{
				Type:     schema.TypeBool,
				Required: false,
				Default:  false,
				Optional: true,
			},
			"native_vlan_tag": &schema.Schema{
				Type:     schema.TypeBool,
				Required: false,
				Default:  false,
				Optional: true,
			},
		},
	}
}

func declaredL2Interface(d *schema.ResourceData) aoscxgo.L2Interface {
	return aoscxgo.L2Interface{
		Interface:       aoscxgo.Interface{Name: d.Get("interface").(string)},
		VlanMode:        d.Get("vlan_mode").(string),
		VlanTag:         d.Get("vlan_tag").(int),
		VlanIds:         d.Get("vlan_ids").(*schema.Set).List(),
		TrunkAllowedAll: d.Get("trunk_allowed_all").(bool),
		NativeVlanTag:   d.Get("native_vlan_tag").(bool),
	}
}

func resourceL2InterfaceCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)

	l2_int := declaredL2Interface(d)
	if err := l2_int.Create(sw); err != nil {
		return diag.Errorf("Error in setting the VLANs of %s: %s", l2_int.Interface.Name, err.(*aoscxgo.RequestError).StatusCode)
	}

	d.SetId(l2_int.Interface.Name)

	return resourceL2InterfaceRead(ctx, d, m)
}

// A port always exists, so a failed read is an error rather than a reason to
// forget the resource and plan its creation again.
func resourceL2InterfaceRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)

	tmp_int := aoscxgo.L2Interface{
		Interface: aoscxgo.Interface{
			Name: d.Get("interface").(string),
		}}

	if err := tmp_int.Get(sw); err != nil {
		return diag.Errorf("Error in reading %s: %s", tmp_int.Interface.Name, err.(*aoscxgo.RequestError).StatusCode)
	}

	vlan_tag := tmp_int.VlanTag
	if vlan_tag == 0 {
		vlan_tag = 1
	}
	if tmp_int.VlanMode == "access" || tmp_int.VlanMode == "" {
		d.Set("vlan_mode", "access")
		d.Set("vlan_tag", vlan_tag)
	} else {
		d.Set("vlan_mode", "trunk")
		d.Set("native_vlan_tag", tmp_int.NativeVlanTag)
		d.Set("trunk_allowed_all", tmp_int.TrunkAllowedAll)
		d.Set("vlan_ids", tmp_int.VlanIds)
		d.Set("vlan_tag", vlan_tag)
	}

	d.Set("interface", tmp_int.Interface.Name)

	return nil
}

func resourceL2InterfaceUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	sw := m.(*aoscxgo.Client)

	l2_int := declaredL2Interface(d)
	if err := l2_int.Update(sw); err != nil {
		return diag.Errorf("Error in setting the VLANs of %s: %s", l2_int.Interface.Name, err.(*aoscxgo.RequestError).StatusCode)
	}

	return resourceL2InterfaceRead(ctx, d, m)
}

func resourceL2InterfaceDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	var err error

	sw := m.(*aoscxgo.Client)

	// Create Interface Obj
	tmp_int := aoscxgo.Interface{
		Name: d.Get("interface").(string),
	}

	err = tmp_int.Delete(sw)

	if err != nil {
		if err.(*aoscxgo.RequestError).StatusCode == "404 Not Found" {
			diags = append(diags, diag.Errorf("Error Updating Interface does not exist: %s", err.(*aoscxgo.RequestError).StatusCode)...)
			return diags
		} else if err.(*aoscxgo.RequestError).StatusCode != "204 No Content" {
			diags = append(diags, diag.Errorf("Error in Updating Interface: %s ", err.(*aoscxgo.RequestError).StatusCode)...)
			return diags
		}
	}

	d.SetId("")
	return nil
}
