/*
*  Copyright (c) WSO2 Inc. (http://www.wso2.org) All Rights Reserved.
*
*  WSO2 Inc. licenses this file to you under the Apache License,
*  Version 2.0 (the "License"); you may not use this file except
*  in compliance with the License.
*  You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing,
* software distributed under the License is distributed on an
* "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
* KIND, either express or implied.  See the License for the
* specific language governing permissions and limitations
* under the License.
 */

package cmd

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"

	"github.com/go-resty/resty"
	"github.com/spf13/cobra"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"

	"net/http"
	"path/filepath"
)

var exportAPIProductName string
var exportAPIProductVersion string
var exportAPIProductProvider string
var exportAPIProductFormat string
var runningExportAPIProductCommand bool

// ExportAPIProduct command related usage info
const exportAPIProductCmdLiteral = "api-product"
const exportAPIProductCmdShortDesc = "Export API Product"

const exportAPIProductCmdLongDesc = "Export an API Product in an environment"

const exportAPIProductCmdExamples = utils.ProjectName + ` ` + exportCmdLiteral + ` ` + exportAPIProductCmdLiteral + ` -n LeasingAPIProduct -e dev
` + utils.ProjectName + ` ` + exportCmdLiteral + ` ` + exportAPIProductCmdLiteral + ` -n CreditAPIProduct -v 1.0.0 -r admin -e production
NOTE: Both the flags (--name (-n) and --environment (-e)) are mandatory`

// ExportAPIProductCmd represents the exportAPIProduct command
var ExportAPIProductCmd = &cobra.Command{
	Use: exportAPIProductCmdLiteral + " (--name <name-of-the-api-product> --provider <provider-of-the-api-product> --environment " +
		"<environment-from-which-the-api-product-should-be-exported>)",
	Short:   exportAPIProductCmdShortDesc,
	Long:    exportAPIProductCmdLongDesc,
	Example: exportAPIProductCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + exportAPIProductCmdLiteral + " called")
		var apiProductsExportDirectory = filepath.Join(utils.ExportDirectory, utils.ExportedApiProductsDirName)

		cred, err := getCredentials(cmdExportEnvironment)
		if err != nil {
			utils.HandleErrorAndExit("Error getting credentials", err)
		}

		executeExportAPIProductCmd(cred, apiProductsExportDirectory)
	},
}

func executeExportAPIProductCmd(credential credentials.Credential, exportDirectory string) {
	runningExportAPIProductCommand = true
	accessToken, preCommandErr := credentials.GetOAuthAccessToken(credential, cmdExportEnvironment)

	if preCommandErr == nil {
		adminEndpoint := utils.GetAdminEndpointOfEnv(cmdExportEnvironment, utils.MainConfigFilePath)
		if exportAPIProductVersion == "" {
			// If the user has not specified the version, use the version as 1.0.0
			exportAPIProductVersion = utils.DefaultApiProductVersion
		}
		resp, err := getExportApiProductResponse(exportAPIProductName, exportAPIProductVersion, exportAPIProductProvider, exportAPIProductFormat, adminEndpoint,
			accessToken)
		if err != nil {
			utils.HandleErrorAndExit("Error while exporting", err)
		}
		// Print info on response
		utils.Logf(utils.LogPrefixInfo+"ResponseStatus: %v\n", resp.Status())
		apiProductZipLocationPath := filepath.Join(exportDirectory, cmdExportEnvironment)
		if resp.StatusCode() == http.StatusOK {
			WriteAPIProductToZip(exportAPIProductName, exportAPIProductVersion, apiProductZipLocationPath, resp)
		} else if resp.StatusCode() == http.StatusInternalServerError {
			// 500 Internal Server Error
			fmt.Println(string(resp.Body()))
		} else {
			// neither 200 nor 500
			fmt.Println("Error exporting API Product:", resp.Status(), "\n", string(resp.Body()))
		}
	} else {
		// error exporting API Product
		fmt.Println("Error getting OAuth tokens while exporting API Product:" + preCommandErr.Error())
	}
}

// WriteAPIProductToZip
// @param exportAPIProductName : Name of the API Product to be exported
// @param resp : Response returned from making the HTTP request (only pass a 200 OK)
// Exported API Product will be written to a zip file
func WriteAPIProductToZip(exportAPIProductName, exportAPIProductVersion, zipLocationPath string, resp *resty.Response) {

	if _, err := os.Stat(zipLocationPath); os.IsNotExist(err) {
		err = os.Mkdir(zipLocationPath, 0777)
		if err != nil {
			utils.HandleErrorAndExit("Error creating zip archive", err)
		}
		// permission 777 : Everyone can read, write, and execute
	}
	zipFilename := exportAPIProductName + "_" + exportAPIProductVersion + ".zip" // MyAPIProduct_1.0.0.zip
	pFile := filepath.Join(zipLocationPath, zipFilename)
	err := ioutil.WriteFile(pFile, resp.Body(), 0644)
	// permission 644 : Only the owner can read and write.. Everyone else can only read.
	if err != nil {
		utils.HandleErrorAndExit("Error creating zip archive", err)
	}
	if runningExportAPIProductCommand {
		fmt.Println("Successfully exported API Product!")
		fmt.Println("Find the exported API Product at " + pFile)
	}
}

// ExportAPIProduct
// @param name : Name of the API Product to be exported
// @param version : Version of the API Product to be exported
// @param provider : Provider of the API Product
// @param adminEndpoint : API Manager Admin Endpoint for the environment
// @param accessToken : Access Token for the resource
// @return response Response in the form of *resty.Response
func getExportApiProductResponse(name, version, provider, format, adminEndpoint, accessToken string) (*resty.Response, error) {
	adminEndpoint = utils.AppendSlashToString(adminEndpoint)
	query := "export/api-product?name=" + name + "&version=" + version + "&providerName=" + provider
	if format != "" {
		query += "&format=" + format
	}

	url := adminEndpoint + query
	utils.Logln(utils.LogPrefixInfo+"ExportAPIProduct: URL:", url)
	headers := make(map[string]string)
	headers[utils.HeaderAuthorization] = utils.HeaderValueAuthBearerPrefix + " " + accessToken
	headers[utils.HeaderAccept] = utils.HeaderValueApplicationZip

	resp, err := utils.InvokeGETRequest(url, headers)

	if err != nil {
		return nil, err
	}

	return resp, nil
}

// init using Cobra
func init() {
	ExportCmd.AddCommand(ExportAPIProductCmd)
	ExportAPIProductCmd.Flags().StringVarP(&exportAPIProductName, "name", "n", "",
		"Name of the API Product to be exported")
	ExportAPIProductCmd.Flags().StringVarP(&exportAPIProductVersion, "version", "v", "",
		"Version of the API Product to be exported")
	ExportAPIProductCmd.Flags().StringVarP(&exportAPIProductProvider, "provider", "r", "",
		"Provider of the API Product")
	ExportAPIProductCmd.Flags().StringVarP(&cmdExportEnvironment, "environment", "e",
		"", "Environment to which the API Product should be exported")
	ExportAPIProductCmd.Flags().StringVarP(&exportAPIProductFormat, "format", "", "", "File format of exported archive (json or yaml)")
	_ = ExportAPIProductCmd.MarkFlagRequired("name")
	_ = ExportAPIProductCmd.MarkFlagRequired("environment")
}
