package azurerm

import (
	"fmt"
	"log"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/arm/disk"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmSnapshot() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmSnapshotCreateUpdate,
		Read:   resourceArmSnapshotRead,
		Update: resourceArmSnapshotCreateUpdate,
		Delete: resourceArmSnapshotDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validateSnapshotName,
			},

			"location": locationSchema(),

			"resource_group_name": resourceGroupNameSchema(),

			"create_option": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(disk.Copy),
					string(disk.Import),
				}, true),
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
			},

			"source_uri": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},

			"source_resource_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"storage_account_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"disk_size_gb": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},

			"encryption_settings": encryptionSettingsSchema(),

			"tags": tagsSchema(),
		},
	}
}

func resourceArmSnapshotCreateUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).snapshotsClient

	name := d.Get("name").(string)
	resourceGroup := d.Get("resource_group_name").(string)
	location := azureRMNormalizeLocation(d.Get("location").(string))
	createOption := d.Get("create_option").(string)
	tags := d.Get("tags").(map[string]interface{})

	properties := disk.Snapshot{
		Location: utils.String(location),
		Properties: &disk.Properties{
			CreationData: &disk.CreationData{
				CreateOption: disk.CreateOption(createOption),
			},
		},
		Tags: expandTags(tags),
	}

	if v, ok := d.GetOk("source_uri"); ok {
		properties.Properties.CreationData.SourceURI = utils.String(v.(string))
	}

	if v, ok := d.GetOk("source_resource_id"); ok {
		properties.Properties.CreationData.SourceResourceID = utils.String(v.(string))
	}

	if v, ok := d.GetOk("storage_account_id"); ok {
		properties.Properties.CreationData.StorageAccountID = utils.String(v.(string))
	}

	diskSizeGB := d.Get("disk_size_gb").(int)
	if diskSizeGB > 0 {
		properties.Properties.DiskSizeGB = utils.Int32(int32(diskSizeGB))
	}

	if v, ok := d.GetOk("encryption_settings"); ok {
		encryptionSettings := v.([]interface{})
		settings := encryptionSettings[0].(map[string]interface{})
		properties.EncryptionSettings = expandManagedDiskEncryptionSettings(settings)
	}

	_, createErr := client.CreateOrUpdate(resourceGroup, name, properties, make(chan struct{}))
	err := <-createErr
	if err != nil {
		return err
	}

	resp, err := client.Get(resourceGroup, name)
	if err != nil {
		return err
	}

	d.SetId(*resp.ID)

	return resourceArmSnapshotRead(d, meta)
}

func resourceArmSnapshotRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).snapshotsClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup := id.ResourceGroup
	name := id.Path["snapshots"]

	resp, err := client.Get(resourceGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[INFO] Error reading Snapshot %q - removing from state", d.Id())
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error making Read request on Snapshot %q: %+v", name, err)
	}

	d.Set("name", resp.Name)
	d.Set("location", azureRMNormalizeLocation(*resp.Location))
	d.Set("resource_group_name", resourceGroup)

	if props := resp.Properties; props != nil {

		if data := props.CreationData; data != nil {
			d.Set("create_option", string(data.CreateOption))

			if data.SourceURI != nil {
				d.Set("source_uri", data.SourceURI)
			}

			if data.SourceResourceID != nil {
				d.Set("source_resource_id", data.SourceResourceID)
			}

			if data.StorageAccountID != nil {
				d.Set("storage_account_id", *data.StorageAccountID)
			}
		}

		if props.DiskSizeGB != nil {
			d.Set("disk_size_gb", int(*props.DiskSizeGB))
		}

		if props.EncryptionSettings != nil {
			d.Set("encryption_settings", flattenManagedDiskEncryptionSettings(props.EncryptionSettings))
		}
	}

	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmSnapshotDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).snapshotsClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup := id.ResourceGroup
	name := id.Path["snapshots"]

	deleteResp, deleteErr := client.Delete(resourceGroup, name, make(chan struct{}))
	resp := <-deleteResp
	err = <-deleteErr
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			return nil
		}

		return fmt.Errorf("Error making Read request on Snapshot %q: %+v", name, err)
	}

	if err != nil {
		return fmt.Errorf("Error deleting Snapshot: %+v", err)
	}

	return nil
}

func validateSnapshotName(v interface{}, k string) (ws []string, errors []error) {
	// a-z, A-Z, 0-9 and _. The max name length is 80
	value := v.(string)

	r, _ := regexp.Compile("^[A-Za-z0-9_]+$")
	if !r.MatchString(value) {
		errors = append(errors, fmt.Errorf("Snapshot Names can only contain alphanumeric characters and underscores."))
	}

	length := len(value)
	if length > 80 {
		errors = append(errors, fmt.Errorf("Snapshot Name can be up to 80 characters, currently %d.", length))
	}

	return
}
