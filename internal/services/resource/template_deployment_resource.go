package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2020-06-01/resources"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/azure"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/tf"
	"github.com/hashicorp/terraform-provider-azurerm/internal/clients"
	"github.com/hashicorp/terraform-provider-azurerm/internal/features"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/suppress"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/internal/timeouts"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
)

func resourceTemplateDeployment() *pluginsdk.Resource {
	return &pluginsdk.Resource{
		Create: resourceTemplateDeploymentCreateUpdate,
		Read:   resourceTemplateDeploymentRead,
		Update: resourceTemplateDeploymentCreateUpdate,
		Delete: resourceTemplateDeploymentDelete,

		Timeouts: &pluginsdk.ResourceTimeout{
			Create: pluginsdk.DefaultTimeout(180 * time.Minute),
			Read:   pluginsdk.DefaultTimeout(5 * time.Minute),
			Update: pluginsdk.DefaultTimeout(180 * time.Minute),
			Delete: pluginsdk.DefaultTimeout(180 * time.Minute),
		},

		DeprecationMessage: features.DeprecatedInThreePointOh("The resource 'azurerm_template_deployment' has been superseded by the 'azurerm_resource_group_template_deployment' pluginsdk."),

		Schema: map[string]*pluginsdk.Schema{
			"name": {
				Type:     pluginsdk.TypeString,
				Required: true,
				ForceNew: true,
			},

			"resource_group_name": azure.SchemaResourceGroupName(),

			"template_body": {
				Type:      pluginsdk.TypeString,
				Optional:  true,
				Computed:  true,
				StateFunc: utils.NormalizeJson,
			},

			"parameters": {
				Type:          pluginsdk.TypeMap,
				Optional:      true,
				ConflictsWith: []string{"parameters_body"},
				Elem: &pluginsdk.Schema{
					Type: pluginsdk.TypeString,
				},
			},

			"parameters_body": {
				Type:          pluginsdk.TypeString,
				Optional:      true,
				StateFunc:     utils.NormalizeJson,
				ConflictsWith: []string{"parameters"},
			},

			"deployment_mode": {
				Type:     pluginsdk.TypeString,
				Required: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(resources.DeploymentModeComplete),
					string(resources.DeploymentModeIncremental),
				}, true),
				DiffSuppressFunc: suppress.CaseDifference,
			},

			"outputs": {
				Type:     pluginsdk.TypeMap,
				Computed: true,
				Elem: &pluginsdk.Schema{
					Type: pluginsdk.TypeString,
				},
			},
		},
	}
}

func resourceTemplateDeploymentCreateUpdate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Resource.DeploymentsClient
	ctx, cancel := timeouts.ForCreateUpdate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	name := d.Get("name").(string)
	resourceGroup := d.Get("resource_group_name").(string)
	deploymentMode := d.Get("deployment_mode").(string)

	if d.IsNewResource() {
		existing, err := client.Get(ctx, resourceGroup, name)
		if err != nil {
			if !utils.ResponseWasNotFound(existing.Response) {
				return fmt.Errorf("checking for presence of existing Template Deployment %s (resource group %s) %v", name, resourceGroup, err)
			}
		}

		if existing.ID != nil && *existing.ID != "" {
			return tf.ImportAsExistsError("azurerm_template_deployment", *existing.ID)
		}
	}

	log.Printf("[INFO] preparing arguments for AzureRM Template Deployment creation.")
	properties := resources.DeploymentProperties{
		Mode: resources.DeploymentMode(deploymentMode),
	}

	if v, ok := d.GetOk("parameters"); ok {
		params := v.(map[string]interface{})

		newParams := make(map[string]interface{}, len(params))
		for key, val := range params {
			newParams[key] = struct {
				Value interface{}
			}{
				Value: val,
			}
		}

		properties.Parameters = &newParams
	}

	if v, ok := d.GetOk("parameters_body"); ok {
		params, err := expandParametersBody(v.(string))
		if err != nil {
			return err
		}

		properties.Parameters = &params
	}

	if v, ok := d.GetOk("template_body"); ok {
		template, err := expandTemplateBody(v.(string))
		if err != nil {
			return err
		}

		properties.Template = &template
	}

	deployment := resources.Deployment{
		Properties: &properties,
	}

	if !d.IsNewResource() {
		d.Partial(true)
	}

	deploymentValidateFuture, err := client.Validate(ctx, resourceGroup, name, deployment)
	if err != nil {
		return fmt.Errorf("requesting Validation for Template Deployment %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if err := deploymentValidateFuture.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("waiting for Validation of Template Deployment %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	validationResult, err := deploymentValidateFuture.Result(*client)
	if err != nil {
		return fmt.Errorf("retrieving Validation of Template Deployment %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if validationResult.Error != nil {
		if validationResult.Error.Message != nil {
			return fmt.Errorf("validating Template for Deployment %q (Resource Group %q): %+v", name, resourceGroup, *validationResult.Error.Message)
		}
		return fmt.Errorf("validating Template for Deployment %q (Resource Group %q): %+v", name, resourceGroup, *validationResult.Error)
	}

	if !d.IsNewResource() {
		d.Partial(false)
	}

	future, err := client.CreateOrUpdate(ctx, resourceGroup, name, deployment)
	if err != nil {
		return fmt.Errorf("creating deployment: %+v", err)
	}

	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("waiting for deployment: %+v", err)
	}

	read, err := client.Get(ctx, resourceGroup, name)
	if err != nil {
		return err
	}
	if read.ID == nil {
		return fmt.Errorf("Cannot read Template Deployment %s (resource group %s) ID", name, resourceGroup)
	}

	d.SetId(*read.ID)

	return resourceTemplateDeploymentRead(d, meta)
}

func resourceTemplateDeploymentRead(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Resource.DeploymentsClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := azure.ParseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resourceGroup := id.ResourceGroup
	name := id.Path["deployments"]
	if name == "" {
		name = id.Path["Deployments"]
	}

	resp, err := client.Get(ctx, resourceGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("making Read request on Azure RM Template Deployment %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	outputs := make(map[string]string)
	if outs := resp.Properties.Outputs; outs != nil {
		outsVal := outs.(map[string]interface{})
		if len(outsVal) > 0 {
			for key, output := range outsVal {
				log.Printf("[DEBUG] Processing deployment output %s", key)
				outputMap := output.(map[string]interface{})
				outputValue, ok := outputMap["value"]
				if !ok {
					log.Printf("[DEBUG] No value - skipping")
					continue
				}
				outputType, ok := outputMap["type"]
				if !ok {
					log.Printf("[DEBUG] No type - skipping")
					continue
				}

				var outputValueString string
				switch strings.ToLower(outputType.(string)) {
				case "bool":
					outputValueString = strconv.FormatBool(outputValue.(bool))

				case "string":
					outputValueString = outputValue.(string)

				case "int":
					outputValueString = fmt.Sprint(outputValue)

				default:
					log.Printf("[WARN] Ignoring output %s: Outputs of type %s are not currently supported in azurerm_template_deployment.",
						key, outputType)
					continue
				}
				outputs[key] = outputValueString
			}
		}
	}

	return d.Set("outputs", outputs)
}

func resourceTemplateDeploymentDelete(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Resource.DeploymentsClient
	ctx, cancel := timeouts.ForDelete(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := azure.ParseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resourceGroup := id.ResourceGroup
	name := id.Path["deployments"]
	if name == "" {
		name = id.Path["Deployments"]
	}

	if _, err = client.Delete(ctx, resourceGroup, name); err != nil {
		return err
	}

	return waitForTemplateDeploymentToBeDeleted(ctx, client, resourceGroup, name, d)
}

// TODO: move this out into the new `helpers` structure
func expandParametersBody(body string) (map[string]interface{}, error) {
	var parametersBody map[string]interface{}
	if err := json.Unmarshal([]byte(body), &parametersBody); err != nil {
		return nil, fmt.Errorf("Expanding the parameters_body for Azure RM Template Deployment")
	}
	return parametersBody, nil
}

func expandTemplateBody(template string) (map[string]interface{}, error) {
	var templateBody map[string]interface{}
	if err := json.Unmarshal([]byte(template), &templateBody); err != nil {
		return nil, fmt.Errorf("Expanding the template_body for Azure RM Template Deployment")
	}
	return templateBody, nil
}

func waitForTemplateDeploymentToBeDeleted(ctx context.Context, client *resources.DeploymentsClient, resourceGroup, name string, d *pluginsdk.ResourceData) error {
	// we can't use the Waiter here since the API returns a 200 once it's deleted which is considered a polling status code..
	log.Printf("[DEBUG] Waiting for Template Deployment (%q in Resource Group %q) to be deleted", name, resourceGroup)
	stateConf := &pluginsdk.StateChangeConf{
		Pending: []string{"200"},
		Target:  []string{"404"},
		Refresh: templateDeploymentStateStatusCodeRefreshFunc(ctx, client, resourceGroup, name),
		Timeout: d.Timeout(pluginsdk.TimeoutDelete),
	}
	if _, err := stateConf.WaitForStateContext(ctx); err != nil {
		return fmt.Errorf("waiting for Template Deployment (%q in Resource Group %q) to be deleted: %+v", name, resourceGroup, err)
	}

	return nil
}

func templateDeploymentStateStatusCodeRefreshFunc(ctx context.Context, client *resources.DeploymentsClient, resourceGroup, name string) pluginsdk.StateRefreshFunc {
	return func() (interface{}, string, error) {
		res, err := client.Get(ctx, resourceGroup, name)

		log.Printf("Retrieving Template Deployment %q (Resource Group %q) returned Status %d", resourceGroup, name, res.StatusCode)

		if err != nil {
			if utils.ResponseWasNotFound(res.Response) {
				return res, strconv.Itoa(res.StatusCode), nil
			}
			return nil, "", fmt.Errorf("polling for the status of the Template Deployment %q (RG: %q): %+v", name, resourceGroup, err)
		}

		return res, strconv.Itoa(res.StatusCode), nil
	}
}