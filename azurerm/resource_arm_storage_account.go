package azurerm

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/arm/storage"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

// The KeySource of storage.Encryption appears to require this value
// for Encryption services to work
var storageAccountEncryptionSource = "Microsoft.Storage"

const blobStorageAccountDefaultAccessTier = "Hot"

func resourceArmStorageAccount() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmStorageAccountCreate,
		Read:   resourceArmStorageAccountRead,
		Update: resourceArmStorageAccountUpdate,
		Delete: resourceArmStorageAccountDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},
		MigrateState:  resourceStorageAccountMigrateState,
		SchemaVersion: 1,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validateArmStorageAccountName,
			},

			"resource_group_name": resourceGroupNameDiffSuppressSchema(),

			"location": locationSchema(),

			"account_kind": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(storage.Storage),
					string(storage.BlobStorage),
				}, true),
				Default: string(storage.Storage),
			},

			"account_type": {
				Type:             schema.TypeString,
				Optional:         true,
				Computed:         true,
				Deprecated:       "This field has been split into `account_tier` and `account_replication_type`",
				ValidateFunc:     validateArmStorageAccountType,
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
			},

			"account_tier": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					"Standard",
					"Premium",
				}, true),
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
			},

			"account_replication_type": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.StringInSlice([]string{
					"LRS",
					"ZRS",
					"GRS",
					"RAGRS",
				}, true),
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
			},

			// Only valid for BlobStorage accounts, defaults to "Hot" in create function
			"access_tier": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(storage.Cool),
					string(storage.Hot),
				}, true),
			},

			"custom_domain": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},

						"use_subdomain": {
							Type:     schema.TypeBool,
							Optional: true,
							Default:  false,
						},
					},
				},
			},

			"enable_blob_encryption": {
				Type:     schema.TypeBool,
				Optional: true,
			},

			"enable_file_encryption": {
				Type:     schema.TypeBool,
				Optional: true,
			},

			"enable_https_traffic_only": {
				Type:     schema.TypeBool,
				Optional: true,
			},

			"primary_location": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_location": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_blob_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_blob_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_queue_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_queue_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_table_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_table_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			// NOTE: The API does not appear to expose a secondary file endpoint
			"primary_file_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_access_key": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_access_key": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_blob_connection_string": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_blob_connection_string": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"tags": tagsSchema(),
		},
	}

}

func resourceArmStorageAccountCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient)
	storageClient := client.storageServiceClient

	resourceGroupName := d.Get("resource_group_name").(string)
	storageAccountName := d.Get("name").(string)
	accountKind := d.Get("account_kind").(string)

	location := d.Get("location").(string)
	tags := d.Get("tags").(map[string]interface{})
	enableBlobEncryption := d.Get("enable_blob_encryption").(bool)
	enableHTTPSTrafficOnly := d.Get("enable_https_traffic_only").(bool)

	accountTier := d.Get("account_tier").(string)
	replicationType := d.Get("account_replication_type").(string)
	storageType := fmt.Sprintf("%s_%s", accountTier, replicationType)

	parameters := storage.AccountCreateParameters{
		Location: &location,
		Sku: &storage.Sku{
			Name: storage.SkuName(storageType),
		},
		Tags: expandTags(tags),
		Kind: storage.Kind(accountKind),
		AccountPropertiesCreateParameters: &storage.AccountPropertiesCreateParameters{
			Encryption: &storage.Encryption{
				Services: &storage.EncryptionServices{
					Blob: &storage.EncryptionService{
						Enabled: utils.Bool(enableBlobEncryption),
					}},
				KeySource: &storageAccountEncryptionSource,
			},
			EnableHTTPSTrafficOnly: &enableHTTPSTrafficOnly,
		},
	}

	if v, ok := d.GetOk("enable_file_encryption"); ok {
		parameters.Encryption.Services.File = &storage.EncryptionService{
			Enabled: utils.Bool(v.(bool)),
		}
	}

	if _, ok := d.GetOk("custom_domain"); ok {
		parameters.CustomDomain = expandStorageAccountCustomDomain(d)
	}

	// AccessTier is only valid for BlobStorage accounts
	if accountKind == string(storage.BlobStorage) {
		if string(parameters.Sku.Name) == string(storage.StandardZRS) {
			return fmt.Errorf("A `account_replication_type` of `ZRS` isn't supported for Blob Storage accounts.")
		}

		accessTier, ok := d.GetOk("access_tier")
		if !ok {
			// default to "Hot"
			accessTier = blobStorageAccountDefaultAccessTier
		}

		parameters.AccountPropertiesCreateParameters.AccessTier = storage.AccessTier(accessTier.(string))
	}

	// Create
	_, createError := storageClient.Create(resourceGroupName, storageAccountName, parameters, make(chan struct{}))
	createErr := <-createError

	// The only way to get the ID back apparently is to read the resource again
	read, err := storageClient.GetProperties(resourceGroupName, storageAccountName)

	// Set the ID right away if we have one
	if err == nil && read.ID != nil {
		log.Printf("[INFO] storage account %q ID: %q", storageAccountName, *read.ID)
		d.SetId(*read.ID)
	}

	// If we had a create error earlier then we return with that error now.
	// We do this later here so that we can grab the ID above is possible.
	if createErr != nil {
		return fmt.Errorf(
			"Error creating Azure Storage Account %q: %+v",
			storageAccountName, createErr)
	}

	// Check the read error now that we know it would exist without a create err
	if err != nil {
		return err
	}

	// If we got no ID then the resource group doesn't yet exist
	if read.ID == nil {
		return fmt.Errorf("Cannot read Storage Account %q (resource group %q) ID",
			storageAccountName, resourceGroupName)
	}

	log.Printf("[DEBUG] Waiting for Storage Account (%s) to become available", storageAccountName)
	stateConf := &resource.StateChangeConf{
		Pending:    []string{"Updating", "Creating"},
		Target:     []string{"Succeeded"},
		Refresh:    storageAccountStateRefreshFunc(client, resourceGroupName, storageAccountName),
		Timeout:    30 * time.Minute,
		MinTimeout: 15 * time.Second,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf("Error waiting for Storage Account (%s) to become available: %s", storageAccountName, err)
	}

	return resourceArmStorageAccountRead(d, meta)
}

// resourceArmStorageAccountUpdate is unusual in the ARM API where most resources have a combined
// and idempotent operation for CreateOrUpdate. In particular updating all of the parameters
// available requires a call to Update per parameter...
func resourceArmStorageAccountUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient
	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	storageAccountName := id.Path["storageAccounts"]
	resourceGroupName := id.ResourceGroup

	accountTier := d.Get("account_tier").(string)
	replicationType := d.Get("account_replication_type").(string)
	storageType := fmt.Sprintf("%s_%s", accountTier, replicationType)
	accountKind := d.Get("account_kind").(string)

	if accountKind == string(storage.BlobStorage) {
		if storageType == string(storage.StandardZRS) {
			return fmt.Errorf("A `account_replication_type` of `ZRS` isn't supported for Blob Storage accounts.")
		}
	}

	d.Partial(true)

	if d.HasChange("account_replication_type") {
		sku := storage.Sku{
			Name: storage.SkuName(storageType),
		}

		opts := storage.AccountUpdateParameters{
			Sku: &sku,
		}
		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account type %q: %+v", storageAccountName, err)
		}

		d.SetPartial("account_replication_type")
	}

	if d.HasChange("access_tier") {
		accessTier := d.Get("access_tier").(string)

		opts := storage.AccountUpdateParameters{
			AccountPropertiesUpdateParameters: &storage.AccountPropertiesUpdateParameters{
				AccessTier: storage.AccessTier(accessTier),
			},
		}

		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account access_tier %q: %+v", storageAccountName, err)
		}

		d.SetPartial("access_tier")
	}

	if d.HasChange("tags") {
		tags := d.Get("tags").(map[string]interface{})

		opts := storage.AccountUpdateParameters{
			Tags: expandTags(tags),
		}
		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account tags %q: %+v", storageAccountName, err)
		}

		d.SetPartial("tags")
	}

	if d.HasChange("enable_blob_encryption") || d.HasChange("enable_file_encryption") {

		opts := storage.AccountUpdateParameters{
			AccountPropertiesUpdateParameters: &storage.AccountPropertiesUpdateParameters{
				Encryption: &storage.Encryption{
					Services:  &storage.EncryptionServices{},
					KeySource: &storageAccountEncryptionSource,
				},
			},
		}

		if d.HasChange("enable_blob_encryption") {
			enableEncryption := d.Get("enable_blob_encryption").(bool)
			opts.Encryption.Services.Blob = &storage.EncryptionService{
				Enabled: utils.Bool(enableEncryption),
			}

			d.SetPartial("enable_blob_encryption")
		}

		if d.HasChange("enable_file_encryption") {
			enableEncryption := d.Get("enable_file_encryption").(bool)
			opts.Encryption.Services.File = &storage.EncryptionService{
				Enabled: utils.Bool(enableEncryption),
			}
			d.SetPartial("enable_file_encryption")
		}

		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account Encryption %q: %+v", storageAccountName, err)
		}
	}

	if d.HasChange("custom_domain") {
		customDomain := expandStorageAccountCustomDomain(d)
		opts := storage.AccountUpdateParameters{
			AccountPropertiesUpdateParameters: &storage.AccountPropertiesUpdateParameters{
				CustomDomain: customDomain,
			},
		}

		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account Custom Domain %q: %+v", storageAccountName, err)
		}
	}

	if d.HasChange("enable_https_traffic_only") {
		enableHTTPSTrafficOnly := d.Get("enable_https_traffic_only").(bool)

		opts := storage.AccountUpdateParameters{
			AccountPropertiesUpdateParameters: &storage.AccountPropertiesUpdateParameters{
				EnableHTTPSTrafficOnly: &enableHTTPSTrafficOnly,
			},
		}
		_, err := client.Update(resourceGroupName, storageAccountName, opts)
		if err != nil {
			return fmt.Errorf("Error updating Azure Storage Account enable_https_traffic_only %q: %+v", storageAccountName, err)
		}

		d.SetPartial("enable_https_traffic_only")
	}

	d.Partial(false)
	return nil
}

func resourceArmStorageAccountRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	name := id.Path["storageAccounts"]
	resGroup := id.ResourceGroup

	resp, err := client.GetProperties(resGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error reading the state of AzureRM Storage Account %q: %+v", name, err)
	}

	keys, err := client.ListKeys(resGroup, name)
	if err != nil {
		return err
	}

	accessKeys := *keys.Keys
	d.Set("name", resp.Name)
	d.Set("resource_group_name", resGroup)
	d.Set("location", azureRMNormalizeLocation(*resp.Location))
	d.Set("account_kind", resp.Kind)

	if sku := resp.Sku; sku != nil {
		d.Set("account_type", sku.Name)
		d.Set("account_tier", sku.Tier)
		d.Set("account_replication_type", strings.Split(fmt.Sprintf("%v", sku.Name), "_")[1])
	}

	if props := resp.AccountProperties; props != nil {
		d.Set("access_tier", props.AccessTier)
		d.Set("enable_https_traffic_only", props.EnableHTTPSTrafficOnly)

		if customDomain := props.CustomDomain; customDomain != nil {
			if err := d.Set("custom_domain", flattenStorageAccountCustomDomain(customDomain)); err != nil {
				return fmt.Errorf("Error flattening `custom_domain`: %+v", err)
			}
		}

		if encryption := props.Encryption; encryption != nil {
			if services := encryption.Services; services != nil {
				if blob := services.Blob; blob != nil {
					d.Set("enable_blob_encryption", blob.Enabled)
				}
				if file := services.File; file != nil {
					d.Set("enable_file_encryption", file.Enabled)
				}
			}
		}

		// Computed
		d.Set("primary_location", props.PrimaryLocation)
		d.Set("secondary_location", props.SecondaryLocation)

		if endpoints := props.PrimaryEndpoints; endpoints != nil {
			d.Set("primary_blob_endpoint", endpoints.Blob)
			d.Set("primary_queue_endpoint", endpoints.Queue)
			d.Set("primary_table_endpoint", endpoints.Table)
			d.Set("primary_file_endpoint", endpoints.File)

			pscs := fmt.Sprintf("DefaultEndpointsProtocol=https;BlobEndpoint=%s;AccountName=%s;AccountKey=%s",
				*endpoints.Blob, *resp.Name, *accessKeys[0].Value)
			d.Set("primary_blob_connection_string", pscs)
		}

		if endpoints := props.SecondaryEndpoints; endpoints != nil {
			if blob := endpoints.Blob; blob != nil {
				d.Set("secondary_blob_endpoint", blob)
				sscs := fmt.Sprintf("DefaultEndpointsProtocol=https;BlobEndpoint=%s;AccountName=%s;AccountKey=%s",
					*blob, *resp.Name, *accessKeys[1].Value)
				d.Set("secondary_blob_connection_string", sscs)
			} else {
				d.Set("secondary_blob_endpoint", "")
				d.Set("secondary_blob_connection_string", "")
			}

			if endpoints.Queue != nil {
				d.Set("secondary_queue_endpoint", endpoints.Queue)
			} else {
				d.Set("secondary_queue_endpoint", "")
			}

			if endpoints.Table != nil {
				d.Set("secondary_table_endpoint", endpoints.Table)
			} else {
				d.Set("secondary_table_endpoint", "")
			}
		}
	}

	d.Set("primary_access_key", accessKeys[0].Value)
	d.Set("secondary_access_key", accessKeys[1].Value)

	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmStorageAccountDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	name := id.Path["storageAccounts"]
	resGroup := id.ResourceGroup

	_, err = client.Delete(resGroup, name)
	if err != nil {
		return fmt.Errorf("Error issuing AzureRM delete request for storage account %q: %+v", name, err)
	}

	return nil
}

func expandStorageAccountCustomDomain(d *schema.ResourceData) *storage.CustomDomain {
	domains := d.Get("custom_domain").([]interface{})
	domain := domains[0].(map[string]interface{})
	name := domain["name"].(string)
	useSubDomain := domain["use_subdomain"].(bool)
	return &storage.CustomDomain{
		Name:         utils.String(name),
		UseSubDomain: utils.Bool(useSubDomain),
	}
}

func flattenStorageAccountCustomDomain(input *storage.CustomDomain) []interface{} {
	domain := make(map[string]interface{}, 0)

	domain["name"] = *input.Name
	// use_subdomain isn't returned

	return []interface{}{domain}
}

func validateArmStorageAccountName(v interface{}, k string) (ws []string, es []error) {
	input := v.(string)

	if !regexp.MustCompile(`\A([a-z0-9]{3,24})\z`).MatchString(input) {
		es = append(es, fmt.Errorf("name can only consist of lowercase letters and numbers, and must be between 3 and 24 characters long"))
	}

	return
}

func validateArmStorageAccountType(v interface{}, k string) (ws []string, es []error) {
	validAccountTypes := []string{"standard_lrs", "standard_zrs",
		"standard_grs", "standard_ragrs", "premium_lrs"}

	input := strings.ToLower(v.(string))

	for _, valid := range validAccountTypes {
		if valid == input {
			return
		}
	}

	es = append(es, fmt.Errorf("Invalid storage account type %q", input))
	return
}

func storageAccountStateRefreshFunc(client *ArmClient, resourceGroupName string, storageAccountName string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		res, err := client.storageServiceClient.GetProperties(resourceGroupName, storageAccountName)
		if err != nil {
			return nil, "", fmt.Errorf("Error issuing read request in storageAccountStateRefreshFunc to Azure ARM for Storage Account '%s' (RG: '%s'): %s", storageAccountName, resourceGroupName, err)
		}

		return res, string(res.AccountProperties.ProvisioningState), nil
	}
}
