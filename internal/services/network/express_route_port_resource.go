// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package network

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/go-azure-helpers/lang/pointer"
	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonschema"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/identity"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/location"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/tags"
	"github.com/hashicorp/go-azure-sdk/resource-manager/network/2024-05-01/expressrouteports"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/tf"
	"github.com/hashicorp/terraform-provider-azurerm/internal/clients"
	"github.com/hashicorp/terraform-provider-azurerm/internal/locks"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/network/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/internal/timeouts"
)

var expressRoutePortSchema = &pluginsdk.Schema{
	Type: pluginsdk.TypeList,
	// Service will always create a pair of links automatically. Users can't add or remove link, but only manipulate existing ones.
	// This is because the link is actually a map to the physical pair of ports on the MS edge device.
	Optional: true,
	Computed: true,
	MinItems: 1,
	MaxItems: 1,
	Elem: &pluginsdk.Resource{
		Schema: map[string]*pluginsdk.Schema{
			"admin_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  false,
			},
			"macsec_cipher": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				Default:  string(expressrouteports.ExpressRouteLinkMacSecCipherGcmAesOneTwoEight),
				ValidateFunc: validation.StringInSlice([]string{
					string(expressrouteports.ExpressRouteLinkMacSecCipherGcmAesOneTwoEight),
					string(expressrouteports.ExpressRouteLinkMacSecCipherGcmAesTwoFiveSix),
					string(expressrouteports.ExpressRouteLinkMacSecCipherGcmAesXpnOneTwoEight),
					string(expressrouteports.ExpressRouteLinkMacSecCipherGcmAesXpnTwoFiveSix),
				}, false),
			},
			"macsec_ckn_keyvault_secret_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},
			"macsec_cak_keyvault_secret_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},
			"macsec_sci_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  false,
			},
			"id": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"router_name": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"interface_name": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"patch_panel_id": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"rack_id": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"connector_type": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
		},
	},
}

func resourceArmExpressRoutePort() *pluginsdk.Resource {
	return &pluginsdk.Resource{
		Create: resourceArmExpressRoutePortCreate,
		Read:   resourceArmExpressRoutePortRead,
		Update: resourceArmExpressRoutePortUpdate,
		Delete: resourceArmExpressRoutePortDelete,

		Importer: pluginsdk.ImporterValidatingResourceId(func(id string) error {
			_, err := expressrouteports.ParseExpressRoutePortID(id)
			return err
		}),

		Timeouts: &pluginsdk.ResourceTimeout{
			Create: pluginsdk.DefaultTimeout(30 * time.Minute),
			Read:   pluginsdk.DefaultTimeout(5 * time.Minute),
			Update: pluginsdk.DefaultTimeout(30 * time.Minute),
			Delete: pluginsdk.DefaultTimeout(30 * time.Minute),
		},

		Schema: map[string]*pluginsdk.Schema{
			"name": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validate.ExpressRoutePortName,
			},

			"resource_group_name": commonschema.ResourceGroupName(),

			"location": commonschema.Location(),

			"peering_location": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},

			"bandwidth_in_gbps": {
				Type:         pluginsdk.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntAtLeast(1),
			},

			"encapsulation": {
				Type:     pluginsdk.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(expressrouteports.ExpressRoutePortsEncapsulationDotOneQ),
					string(expressrouteports.ExpressRoutePortsEncapsulationQinQ),
				}, false),
			},

			"identity": commonschema.SystemAssignedUserAssignedIdentityOptional(),

			"billing_type": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				Default:  string(expressrouteports.ExpressRoutePortsBillingTypeMeteredData),
				ValidateFunc: validation.StringInSlice([]string{
					string(expressrouteports.ExpressRoutePortsBillingTypeMeteredData),
					string(expressrouteports.ExpressRoutePortsBillingTypeUnlimitedData),
				}, false),
			},

			"link1": expressRoutePortSchema,

			"link2": expressRoutePortSchema,

			"ethertype": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},

			"guid": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},

			"mtu": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},

			"tags": commonschema.Tags(),
		},
	}
}

func resourceArmExpressRoutePortCreate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.ExpressRoutePorts
	subscriptionId := meta.(*clients.Client).Account.SubscriptionId
	ctx, cancel := timeouts.ForCreate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id := expressrouteports.NewExpressRoutePortID(subscriptionId, d.Get("resource_group_name").(string), d.Get("name").(string))

	resp, err := client.Get(ctx, id)
	if err != nil {
		if !response.WasNotFound(resp.HttpResponse) {
			return fmt.Errorf("checking for existing %s: %+v", id, err)
		}
	}

	if !response.WasNotFound(resp.HttpResponse) {
		return tf.ImportAsExistsError("azurerm_express_route_port", id.ID())
	}

	log.Printf("[DEBUG] ERP-DEBUG-CREATE: Terraform state identity value: %+v", d.Get("identity"))
	expandedIdentity, err := identity.ExpandSystemAndUserAssignedMap(d.Get("identity").([]interface{}))
	if err != nil {
		return fmt.Errorf("expanding `identity`: %+v", err)
	}
	log.Printf("[DEBUG] ERP-DEBUG-CREATE: Expanded identity for API: %+v", expandedIdentity)
	if expandedIdentity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: Expanded identity Type: %q", string(expandedIdentity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: Expanded identity IdentityIds: %+v", expandedIdentity.IdentityIds)
	}

	param := expressrouteports.ExpressRoutePort{
		Name:     pointer.To(id.ExpressRoutePortName),
		Location: pointer.To(location.Normalize(d.Get("location").(string))),
		Properties: &expressrouteports.ExpressRoutePortPropertiesFormat{
			PeeringLocation: pointer.To(d.Get("peering_location").(string)),
			BandwidthInGbps: pointer.To(int64(d.Get("bandwidth_in_gbps").(int))),
			Encapsulation:   pointer.To(expressrouteports.ExpressRoutePortsEncapsulation(d.Get("encapsulation").(string))),
		},
		Identity: expandedIdentity,
		Tags:     tags.Expand(d.Get("tags").(map[string]interface{})),
	}

	if v, ok := d.GetOk("billing_type"); ok {
		param.Properties.BillingType = pointer.To(expressrouteports.ExpressRoutePortsBillingType(v.(string)))
	}

	// a lock is needed here for subresource express_route_port_authorization needs a lock.
	locks.ByID(id.ID())
	defer locks.UnlockByID(id.ID())

	// The link properties can't be specified in first creation. It will result into either error (e.g. setting `adminState`) or being ignored (e.g. setting MACSec)
	log.Printf("[DEBUG] ERP-DEBUG-CREATE: About to send first CREATE request with Identity: %+v", param.Identity)
	if err := client.CreateOrUpdateThenPoll(ctx, id, param); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
	}

	// Verify what was actually created by doing a GET
	log.Printf("[DEBUG] ERP-DEBUG-CREATE: First CREATE completed, verifying identity...")
	verifyResp, err := client.Get(ctx, id)
	if err != nil {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: Failed to verify after first CREATE: %+v", err)
	} else if verifyResp.Model != nil && verifyResp.Model.Identity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After first CREATE - Identity from API: %+v", verifyResp.Model.Identity)
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After first CREATE - Identity.Type: %q", string(verifyResp.Model.Identity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After first CREATE - Identity.IdentityIds: %+v", verifyResp.Model.Identity.IdentityIds)
	} else {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After first CREATE - No identity returned from API")
	}

	param.Properties.Links = expandExpressRoutePortLinks(d.Get("link1").([]interface{}), d.Get("link2").([]interface{}))

	log.Printf("[DEBUG] ERP-DEBUG-CREATE: About to send second CREATE (with links) with Identity: %+v", param.Identity)
	if err := client.CreateOrUpdateThenPoll(ctx, id, param); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
	}

	// Verify what was actually created after links update
	log.Printf("[DEBUG] ERP-DEBUG-CREATE: Second CREATE completed, verifying identity...")
	verifyResp2, err := client.Get(ctx, id)
	if err != nil {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: Failed to verify after second CREATE: %+v", err)
	} else if verifyResp2.Model != nil && verifyResp2.Model.Identity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After second CREATE - Identity from API: %+v", verifyResp2.Model.Identity)
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After second CREATE - Identity.Type: %q", string(verifyResp2.Model.Identity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After second CREATE - Identity.IdentityIds: %+v", verifyResp2.Model.Identity.IdentityIds)
	} else {
		log.Printf("[DEBUG] ERP-DEBUG-CREATE: After second CREATE - No identity returned from API")
	}

	d.SetId(id.ID())

	return resourceArmExpressRoutePortRead(d, meta)
}

func resourceArmExpressRoutePortUpdate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.ExpressRoutePorts
	ctx, cancel := timeouts.ForUpdate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := expressrouteports.ParseExpressRoutePortID(d.Id())
	if err != nil {
		return err
	}

	existing, err := client.Get(ctx, *id)
	if err != nil {
		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	if existing.Model == nil {
		return fmt.Errorf("retrieving %s: `model` was nil", *id)
	}
	if existing.Model.Properties == nil {
		return fmt.Errorf("retrieving %s: `properties` was nil", *id)
	}

	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Existing identity from GET request: %+v", existing.Model.Identity)
	if existing.Model.Identity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Existing identity Type: %q", string(existing.Model.Identity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Existing identity IdentityIds: %+v", existing.Model.Identity.IdentityIds)
	}

	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Terraform state identity value: %+v", d.Get("identity"))
	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: d.HasChange('identity'): %v", d.HasChange("identity"))

	payload := existing.Model

	// Only update identity if it has actually changed in the configuration
	// This respects ignore_changes = [identity] by preserving existing identity
	if d.HasChange("identity") {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Identity has changed, expanding from state...")
		expandedIdentity, err := identity.ExpandSystemAndUserAssignedMap(d.Get("identity").([]interface{}))
		if err != nil {
			return fmt.Errorf("expanding `identity`: %+v", err)
		}
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Expanded identity: %+v", expandedIdentity)
		if expandedIdentity != nil {
			log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Expanded identity Type: %q", string(expandedIdentity.Type))
			log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Expanded identity IdentityIds: %+v", expandedIdentity.IdentityIds)
		}
		payload.Identity = expandedIdentity
	} else {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Identity has NOT changed, keeping existing identity")
	}
	// If identity hasn't changed, payload.Identity keeps existing.Model.Identity from line 268
	
	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Final payload identity before API call: %+v", payload.Identity)
	if payload.Identity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Final payload identity Type: %q", string(payload.Identity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Final payload identity IdentityIds: %+v", payload.Identity.IdentityIds)
	}

	if d.HasChange("billing_type") {
		if v, ok := d.GetOk("billing_type"); ok {
			payload.Properties.BillingType = pointer.To(expressrouteports.ExpressRoutePortsBillingType(v.(string)))
		}
	}

	if d.HasChanges("link1", "link2") {
		payload.Properties.Links = expandExpressRoutePortLinks(d.Get("link1").([]interface{}), d.Get("link2").([]interface{}))
	}

	if d.HasChange("tags") {
		payload.Tags = tags.Expand(d.Get("tags").(map[string]interface{}))
	}

	// a lock is needed here for subresource express_route_port_authorization needs a lock.
	locks.ByID(id.ID())
	defer locks.UnlockByID(id.ID())

	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: About to send UPDATE request...")
	if err := client.CreateOrUpdateThenPoll(ctx, *id, *payload); err != nil {
		return fmt.Errorf("updating %s: %+v", id, err)
	}

	// Verify what was actually updated by doing a GET
	log.Printf("[DEBUG] ERP-DEBUG-UPDATE: UPDATE completed, verifying identity...")
	verifyResp, err := client.Get(ctx, *id)
	if err != nil {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: Failed to verify after UPDATE: %+v", err)
	} else if verifyResp.Model != nil && verifyResp.Model.Identity != nil {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: After UPDATE - Identity from API: %+v", verifyResp.Model.Identity)
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: After UPDATE - Identity.Type: %q", string(verifyResp.Model.Identity.Type))
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: After UPDATE - Identity.IdentityIds: %+v", verifyResp.Model.Identity.IdentityIds)
	} else {
		log.Printf("[DEBUG] ERP-DEBUG-UPDATE: After UPDATE - No identity returned from API")
	}

	d.SetId(id.ID())

	return resourceArmExpressRoutePortRead(d, meta)
}

func resourceArmExpressRoutePortRead(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.ExpressRoutePorts
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := expressrouteports.ParseExpressRoutePortID(d.Id())
	if err != nil {
		return err
	}

	resp, err := client.Get(ctx, *id)
	if err != nil {
		if response.WasNotFound(resp.HttpResponse) {
			log.Printf("[DEBUG] %q was not found - removing from state!", id)
			d.SetId("")
			return nil
		}
		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	d.Set("name", id.ExpressRoutePortName)
	d.Set("resource_group_name", id.ResourceGroupName)

	if model := resp.Model; model != nil {
		d.Set("location", location.NormalizeNilable(model.Location))
		
		log.Printf("[DEBUG] ERP-DEBUG-READ: Raw identity from API: %+v", model.Identity)
		if model.Identity != nil {
			log.Printf("[DEBUG] ERP-DEBUG-READ: Raw identity Type: %q", string(model.Identity.Type))
			log.Printf("[DEBUG] ERP-DEBUG-READ: Raw identity PrincipalId: %q", model.Identity.PrincipalId)
			log.Printf("[DEBUG] ERP-DEBUG-READ: Raw identity TenantId: %q", model.Identity.TenantId)
			log.Printf("[DEBUG] ERP-DEBUG-READ: Raw identity IdentityIds: %+v", model.Identity.IdentityIds)
		}
		
		flattenedIdentity, err := identity.FlattenSystemAndUserAssignedMap(model.Identity)
		if err != nil {
			return fmt.Errorf("flattening `identity`: %+v", err)
		}
		
		log.Printf("[DEBUG] ERP-DEBUG-READ: Flattened identity for Terraform state: %+v", flattenedIdentity)
		if flattenedIdentity != nil && len(*flattenedIdentity) > 0 {
			if identityMap, ok := (*flattenedIdentity)[0].(map[string]interface{}); ok {
				log.Printf("[DEBUG] ERP-DEBUG-READ: Flattened identity type: %q", identityMap["type"])
				log.Printf("[DEBUG] ERP-DEBUG-READ: Flattened identity identity_ids: %+v", identityMap["identity_ids"])
				log.Printf("[DEBUG] ERP-DEBUG-READ: Flattened identity principal_id: %q", identityMap["principal_id"])
				log.Printf("[DEBUG] ERP-DEBUG-READ: Flattened identity tenant_id: %q", identityMap["tenant_id"])
			}
		}

		if err := d.Set("identity", flattenedIdentity); err != nil {
			return fmt.Errorf("setting `identity`: %v", err)
		}
		
		log.Printf("[DEBUG] ERP-DEBUG-READ: After d.Set - Terraform state identity value: %+v", d.Get("identity"))

		if props := model.Properties; props != nil {
			d.Set("peering_location", props.PeeringLocation)
			d.Set("bandwidth_in_gbps", props.BandwidthInGbps)
			d.Set("encapsulation", string(pointer.From(props.Encapsulation)))
			d.Set("billing_type", string(pointer.From(props.BillingType)))
			link1, link2, err := flattenExpressRoutePortLinks(props.Links)
			if err != nil {
				return fmt.Errorf("flattening links: %v", err)
			}
			if err := d.Set("link1", link1); err != nil {
				return fmt.Errorf("setting `link1`: %v", err)
			}
			if err := d.Set("link2", link2); err != nil {
				return fmt.Errorf("setting `link2`: %v", err)
			}
			d.Set("ethertype", props.EtherType)
			d.Set("guid", props.ResourceGuid)
			d.Set("mtu", props.Mtu)
		}
		return tags.FlattenAndSet(d, model.Tags)
	}
	return nil
}

func resourceArmExpressRoutePortDelete(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.ExpressRoutePorts
	ctx, cancel := timeouts.ForDelete(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := expressrouteports.ParseExpressRoutePortID(d.Id())
	if err != nil {
		return err
	}

	// a lock is needed here for subresource express_route_port_authorization needs a lock.
	locks.ByID(id.ID())
	defer locks.UnlockByID(id.ID())

	if err := client.DeleteThenPoll(ctx, *id); err != nil {
		return fmt.Errorf("deleting %s: %+v", id, err)
	}

	return nil
}

func expandExpressRoutePortLinks(link1, link2 []interface{}) *[]expressrouteports.ExpressRouteLink {
	var out []expressrouteports.ExpressRouteLink
	if link := expandExpressRoutePortLink(1, link1); link != nil {
		out = append(out, *link)
	}
	if link := expandExpressRoutePortLink(2, link2); link != nil {
		out = append(out, *link)
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

func expandExpressRoutePortLink(idx int, input []interface{}) *expressrouteports.ExpressRouteLink {
	if len(input) == 0 {
		return nil
	}

	b := input[0].(map[string]interface{})
	adminState := expressrouteports.ExpressRouteLinkAdminStateDisabled
	if b["admin_enabled"].(bool) {
		adminState = expressrouteports.ExpressRouteLinkAdminStateEnabled
	}

	sciState := expressrouteports.ExpressRouteLinkMacSecSciStateDisabled
	if b["macsec_sci_enabled"].(bool) {
		sciState = expressrouteports.ExpressRouteLinkMacSecSciStateEnabled
	}

	link := expressrouteports.ExpressRouteLink{
		// The link name is fixed
		Name: pointer.To(fmt.Sprintf("link%d", idx)),
		Properties: &expressrouteports.ExpressRouteLinkPropertiesFormat{
			AdminState: pointer.To(adminState),
			MacSecConfig: &expressrouteports.ExpressRouteLinkMacSecConfig{
				Cipher:   pointer.To(expressrouteports.ExpressRouteLinkMacSecCipher(b["macsec_cipher"].(string))),
				SciState: pointer.To(sciState),
			},
		},
	}

	if cknSecretId := b["macsec_ckn_keyvault_secret_id"].(string); cknSecretId != "" {
		link.Properties.MacSecConfig.CknSecretIdentifier = &cknSecretId
	}
	if cakSecretId := b["macsec_cak_keyvault_secret_id"].(string); cakSecretId != "" {
		link.Properties.MacSecConfig.CakSecretIdentifier = &cakSecretId
	}
	return &link
}

func flattenExpressRoutePortLinks(links *[]expressrouteports.ExpressRouteLink) ([]interface{}, []interface{}, error) {
	if links == nil {
		return nil, nil, nil
	}
	length := len(*links)
	if length != 2 {
		return nil, nil, fmt.Errorf("expected two links, but got %d", length)
	}

	return flattenExpressRoutePortLink((*links)[0]), flattenExpressRoutePortLink((*links)[1]), nil
}

func flattenExpressRoutePortLink(link expressrouteports.ExpressRouteLink) []interface{} {
	var id string
	if link.Id != nil {
		id = *link.Id
	}

	var (
		routerName    string
		interfaceName string
		patchPanelId  string
		rackId        string
		connectorType string
		adminState    bool
		cknSecretId   string
		cakSecretId   string
		cipher        string
		sciState      bool
	)

	if props := link.Properties; props != nil {
		if props.RouterName != nil {
			routerName = *props.RouterName
		}
		if props.InterfaceName != nil {
			interfaceName = *props.InterfaceName
		}
		if props.PatchPanelId != nil {
			patchPanelId = *props.PatchPanelId
		}
		if props.RackId != nil {
			rackId = *props.RackId
		}
		connectorType = string(pointer.From(props.ConnectorType))
		adminState = pointer.From(props.AdminState) == expressrouteports.ExpressRouteLinkAdminStateEnabled
		sciState = pointer.From(props.MacSecConfig.SciState) == expressrouteports.ExpressRouteLinkMacSecSciStateEnabled
		if cfg := props.MacSecConfig; cfg != nil {
			if cfg.CknSecretIdentifier != nil {
				cknSecretId = *cfg.CknSecretIdentifier
			}
			if cfg.CakSecretIdentifier != nil {
				cakSecretId = *cfg.CakSecretIdentifier
			}
			cipher = string(pointer.From(cfg.Cipher))
		}
	}

	return []interface{}{
		map[string]interface{}{
			"id":                            id,
			"router_name":                   routerName,
			"interface_name":                interfaceName,
			"patch_panel_id":                patchPanelId,
			"rack_id":                       rackId,
			"connector_type":                connectorType,
			"admin_enabled":                 adminState,
			"macsec_ckn_keyvault_secret_id": cknSecretId,
			"macsec_cak_keyvault_secret_id": cakSecretId,
			"macsec_cipher":                 cipher,
			"macsec_sci_enabled":            sciState,
		},
	}
}
