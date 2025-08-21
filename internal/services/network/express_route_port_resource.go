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

	// [ERP-DEBUG] Log identity during create
	terraformIdentityCreate := d.Get("identity").([]interface{})
	log.Printf("[DEBUG] ERP-DEBUG-CREATE-TF-IDENTITY: Identity from Terraform state during create: %+v", terraformIdentityCreate)

	expandedIdentity, err := identity.ExpandSystemAndUserAssignedMap(terraformIdentityCreate)
	if err != nil {
		return fmt.Errorf("expanding `identity`: %+v", err)
	}

	log.Printf("[DEBUG] ERP-DEBUG-CREATE-EXPANDED: Expanded identity during create: %+v", expandedIdentity)
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

	// [ERP-DEBUG] Log final create payload identity
	log.Printf("[DEBUG] ERP-DEBUG-CREATE-PAYLOAD: Final create payload identity: %+v", param.Identity)

	if v, ok := d.GetOk("billing_type"); ok {
		param.Properties.BillingType = pointer.To(expressrouteports.ExpressRoutePortsBillingType(v.(string)))
	}

	// a lock is needed here for subresource express_route_port_authorization needs a lock.
	locks.ByID(id.ID())
	defer locks.UnlockByID(id.ID())

	// The link properties can't be specified in first creation. It will result into either error (e.g. setting `adminState`) or being ignored (e.g. setting MACSec)
	if err := client.CreateOrUpdateThenPoll(ctx, id, param); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
	}

	param.Properties.Links = expandExpressRoutePortLinks(d.Get("link1").([]interface{}), d.Get("link2").([]interface{}))

	if err := client.CreateOrUpdateThenPoll(ctx, id, param); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
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

	// [ERP-DEBUG] GET current state from Azure before any changes
	log.Printf("[DEBUG] ERP-DEBUG-GET: About to retrieve current state for %s", id)
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

	// [ERP-DEBUG] Log current Azure identity state
	log.Printf("[DEBUG] ERP-DEBUG-AZURE-IDENTITY: Current identity from Azure: %+v", existing.Model.Identity)

	payload := existing.Model

	// [ERP-DEBUG] Log Terraform state identity
	terraformIdentity := d.Get("identity").([]interface{})
	log.Printf("[DEBUG] ERP-DEBUG-TF-IDENTITY: Identity from Terraform state: %+v", terraformIdentity)

	// [ERP-DEBUG] Check if identity has changed
	hasIdentityChange := d.HasChange("identity")
	log.Printf("[DEBUG] ERP-DEBUG-HAS-CHANGE: d.HasChange('identity') = %t", hasIdentityChange)

	if hasIdentityChange {
		log.Printf("[DEBUG] ERP-DEBUG-EXPANDING: Identity has changed, expanding from Terraform state")
		expandedIdentity, err := identity.ExpandSystemAndUserAssignedMap(terraformIdentity)
		if err != nil {
			return fmt.Errorf("expanding `identity`: %+v", err)
		}
		log.Printf("[DEBUG] ERP-DEBUG-EXPANDED: Expanded identity: %+v", expandedIdentity)
		payload.Identity = expandedIdentity
	} else {
		log.Printf("[DEBUG] ERP-DEBUG-OMITTING: Identity hasn't changed, setting to nil to omit from request")
		// When identity hasn't changed, completely omit it from the request
		// This allows Azure to preserve the existing identity and supports ignore_changes
		payload.Identity = nil
	}

	// [ERP-DEBUG] Log final payload identity
	log.Printf("[DEBUG] ERP-DEBUG-FINAL-PAYLOAD: Final payload identity before PUT: %+v", payload.Identity)

	if d.HasChange("billing_type") {
		if v, ok := d.GetOk("billing_type"); ok {
			payload.Properties.BillingType = pointer.To(expressrouteports.ExpressRoutePortsBillingType(v.(string)))
		}
	}

	if d.HasChanges("link1", "link2") {
		payload.Properties.Links = expandExpressRoutePortLinks(d.Get("link1").([]interface{}), d.Get("link2").([]interface{}))
	}

	// a lock is needed here for subresource express_route_port_authorization needs a lock.
	locks.ByID(id.ID())
	defer locks.UnlockByID(id.ID())

	// Check if only tags have changed - use PATCH UpdateTags API to preserve identity
	if d.HasChange("tags") && !d.HasChangesExcept("tags") {
		log.Printf("[DEBUG] ERP-DEBUG-TAGS-ONLY: Only tags changed, using UpdateTags PATCH API to preserve identity")
		tagsPayload := expressrouteports.TagsObject{
			Tags: tags.Expand(d.Get("tags").(map[string]interface{})),
		}
		if _, err := client.UpdateTags(ctx, *id, tagsPayload); err != nil {
			return fmt.Errorf("updating tags for %s: %+v", id, err)
		}
	} else {
		// For other changes, use the full PUT API
		log.Printf("[DEBUG] ERP-DEBUG-FULL-UPDATE: Non-tag changes detected, using CreateOrUpdate PUT API")
		if d.HasChange("tags") {
			payload.Tags = tags.Expand(d.Get("tags").(map[string]interface{}))
		}
		if err := client.CreateOrUpdateThenPoll(ctx, *id, *payload); err != nil {
			return fmt.Errorf("updating %s: %+v", id, err)
		}
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

	// [ERP-DEBUG] Log read operation start with context
	log.Printf("[DEBUG] ERP-DEBUG-READ-START: Starting read operation for %s", id)
	log.Printf("[DEBUG] ERP-DEBUG-READ-CONTEXT: Current Terraform state ID: %s", d.Id())

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
		// [ERP-DEBUG] Log raw identity from Azure during read
		log.Printf("[DEBUG] ERP-DEBUG-READ-AZURE-IDENTITY: Raw identity from Azure during read: %+v", model.Identity)

		d.Set("location", location.NormalizeNilable(model.Location))
		flattenedIdentity, err := identity.FlattenSystemAndUserAssignedMap(model.Identity)
		if err != nil {
			return fmt.Errorf("flattening `identity`: %+v", err)
		}

		// [ERP-DEBUG] Log flattened identity being set to state
		log.Printf("[DEBUG] ERP-DEBUG-READ-FLATTENED: Flattened identity being set to Terraform state: %+v", flattenedIdentity)

		if err := d.Set("identity", flattenedIdentity); err != nil {
			return fmt.Errorf("setting `identity`: %v", err)
		}

		// [ERP-DEBUG] Verify what actually got set in the state
		stateIdentityAfterSet := d.Get("identity").([]interface{})
		log.Printf("[DEBUG] ERP-DEBUG-READ-STATE-VERIFY: Identity actually stored in state after d.Set: %+v", stateIdentityAfterSet)

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

		// [ERP-DEBUG] Log completion of read operation
		log.Printf("[DEBUG] ERP-DEBUG-READ-COMPLETE: Read operation completed successfully for %s", id)
		return tags.FlattenAndSet(d, model.Tags)
	}

	// [ERP-DEBUG] Log when model is nil
	log.Printf("[DEBUG] ERP-DEBUG-READ-NO-MODEL: No model returned from Azure for %s", id)
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
