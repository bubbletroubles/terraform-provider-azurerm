// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package network_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/go-azure-sdk/resource-manager/network/2024-05-01/publicipprefixes"
	"github.com/hashicorp/terraform-provider-azurerm/internal/acceptance"
	"github.com/hashicorp/terraform-provider-azurerm/internal/acceptance/check"
)

type PublicIPPrefixDataSource struct{}

func TestAccDataSourcePublicIPPrefix_basic(t *testing.T) {
	data := acceptance.BuildTestData(t, "data.azurerm_public_ip_prefix", "test")
	r := PublicIPPrefixDataSource{}
	name := fmt.Sprintf("acctestpublicipprefix-%d", data.RandomInteger)
	resourceGroupName := fmt.Sprintf("acctestRG-%d", data.RandomInteger)

	data.DataSourceTest(t, []acceptance.TestStep{
		{
			Config: r.basic(name, resourceGroupName, data),
			Check: acceptance.ComposeTestCheckFunc(
				check.That(data.ResourceName).Key("name").HasValue(name),
				check.That(data.ResourceName).Key("resource_group_name").HasValue(resourceGroupName),
				check.That(data.ResourceName).Key("location").HasValue(data.Locations.Primary),
				check.That(data.ResourceName).Key("sku").HasValue("Standard"),
				check.That(data.ResourceName).Key("sku_tier").HasValue(string(publicipprefixes.PublicIPPrefixSkuTierRegional)),
				check.That(data.ResourceName).Key("prefix_length").HasValue("31"),
				check.That(data.ResourceName).Key("ip_prefix").Exists(),
				check.That(data.ResourceName).Key("tags.%").HasValue("1"),
				check.That(data.ResourceName).Key("tags.env").HasValue("test"),
			),
		},
	})
}

func TestAccDataSourcePublicIPPrefix_withCustomIPPrefix(t *testing.T) {
	data := acceptance.BuildTestData(t, "data.azurerm_public_ip_prefix", "test")
	r := PublicIPPrefixDataSource{}
	name := fmt.Sprintf("acctestpublicipprefix-%d", data.RandomInteger)
	resourceGroupName := fmt.Sprintf("acctestRG-%d", data.RandomInteger)

	data.DataSourceTest(t, []acceptance.TestStep{
		{
			Config: r.withCustomIPPrefix(name, resourceGroupName, data),
			Check: acceptance.ComposeTestCheckFunc(
				check.That(data.ResourceName).Key("custom_ip_prefix").Exists(),
			),
		},
	})
}

func (PublicIPPrefixDataSource) basic(name string, resourceGroupName string, data acceptance.TestData) string {
	return fmt.Sprintf(`
provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "test" {
  name     = "%s"
  location = "%s"

  tags = {
    env = "test"
  }
}

resource "azurerm_public_ip_prefix" "test" {
  name                = "%s"
  location            = azurerm_resource_group.test.location
  resource_group_name = azurerm_resource_group.test.name
  sku                 = "Standard"
  prefix_length       = 31

  tags = {
    env = "test"
  }
}

data "azurerm_public_ip_prefix" "test" {
  name                = azurerm_public_ip_prefix.test.name
  resource_group_name = azurerm_resource_group.test.name
}
`, resourceGroupName, data.Locations.Primary, name)
}

func (PublicIPPrefixDataSource) withCustomIPPrefix(name string, resourceGroupName string, data acceptance.TestData) string {
	return fmt.Sprintf(`
provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "test" {
  name     = "%s"
  location = "%s"
}

resource "azurerm_custom_ip_prefix" "test" {
  name                = "acctestcustomipprefix-%[4]d"
  resource_group_name = azurerm_resource_group.test.name
  location            = azurerm_resource_group.test.location
  cidr                = "192.168.0.0/24"
  zones               = ["1"]
}

resource "azurerm_public_ip_prefix" "test" {
  name                = "%s"
  location            = azurerm_resource_group.test.location
  resource_group_name = azurerm_resource_group.test.name
  prefix_length       = 28
  custom_ip_prefix    = azurerm_custom_ip_prefix.test.id
}

data "azurerm_public_ip_prefix" "test" {
  name                = azurerm_public_ip_prefix.test.name
  resource_group_name = azurerm_resource_group.test.name
}
`, resourceGroupName, data.Locations.Primary, name, data.RandomInteger)
}
