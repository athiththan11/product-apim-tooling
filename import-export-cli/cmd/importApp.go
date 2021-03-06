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
	"github.com/spf13/cobra"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/impl"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
	"net/http"
)

var importAppFile string
var importAppEnvironment string
var importAppOwner string
var preserveOwner bool
var skipSubscriptions bool
var importAppSkipKeys bool
var importAppUpdateApplication bool

// ImportApp command related usage info
const importAppCmdLiteral = "import-app"
const importAppCmdShortDesc = "Import App"

const importAppCmdLongDesc = "Import an Application to an environment"

const importAppCmdExamples = utils.ProjectName + ` ` + importAppCmdLiteral + ` -f qa/apps/sampleApp.zip -e dev
` + utils.ProjectName + ` ` + importAppCmdLiteral + ` -f staging/apps/sampleApp.zip -e prod -o testUser
` + utils.ProjectName + ` ` + importAppCmdLiteral + ` -f qa/apps/sampleApp.zip --preserveOwner --skipSubscriptions -e prod
NOTE: Both the flags (--file (-f) and --environment (-e)) are mandatory`

// importAppCmd represents the importApp command
var ImportAppCmd = &cobra.Command{
	Use: importAppCmdLiteral + " (--file <app-zip-file> --environment " +
		"<environment-to-which-the-app-should-be-imported>)",
	Short:   importAppCmdShortDesc,
	Long:    importAppCmdLongDesc,
	Example: importAppCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + importAppCmdLiteral + " called")
		cred, err := getCredentials(importAppEnvironment)
		if err != nil {
			utils.HandleErrorAndExit("Error getting credentials", err)
		}
		executeImportAppCmd(cred)
	},
}

func executeImportAppCmd(credential credentials.Credential) {
	accessToken, err := credentials.GetOAuthAccessToken(credential, importAppEnvironment)
	if err != nil {
		utils.HandleErrorAndExit("Error getting OAuth Tokens", err)
	}
	resp, err := impl.ImportApplicationToEnv(accessToken, importAppEnvironment, importAppFile, importAppOwner,
		importAppUpdateApplication, preserveOwner, skipSubscriptions, importAppSkipKeys)
	if err != nil {
		utils.HandleErrorAndExit("Error importing Application", err)
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		// 200 OK or 201 Created
		utils.Logln(utils.LogPrefixInfo+"Header:", resp.Header)
		fmt.Println("Successfully imported Application!")
	} else if resp.StatusCode == http.StatusMultiStatus {
		// 207 Multi Status
		fmt.Printf("\nPartially imported Application" +
			"\nNOTE: One or more subscriptions were not imported due to unavailability of APIs/Tiers\n")
	} else if resp.StatusCode == http.StatusUnauthorized {
		// 401 Unauthorized
		fmt.Println("Invalid Credentials or You may not have enough permission!")
	} else if resp.StatusCode == http.StatusForbidden {
		// 401 Unauthorized
		fmt.Printf("Invalid Owner!" + "\nNOTE: Cross Tenant Imports are not allowed!\n")
	} else {
		fmt.Println("Error importing Application")
		utils.Logln(utils.LogPrefixError + resp.Status)
	}
}

func init() {
	RootCmd.AddCommand(ImportAppCmd)
	ImportAppCmd.Flags().StringVarP(&importAppFile, "file", "f", "",
		"Name of the ZIP file of the Application to be imported")
	ImportAppCmd.Flags().StringVarP(&importAppOwner, "owner", "o", "",
		"Name of the target owner of the Application as desired by the Importer")
	ImportAppCmd.Flags().StringVarP(&importAppEnvironment, "environment", "e",
		"", "Environment from the which the Application should be imported")
	ImportAppCmd.Flags().BoolVarP(&preserveOwner, "preserveOwner", "", false,
		"Preserves app owner")
	ImportAppCmd.Flags().BoolVarP(&skipSubscriptions, "skipSubscriptions", "s", false,
		"Skip subscriptions of the Application")
	ImportAppCmd.Flags().BoolVarP(&importAppSkipKeys, "skipKeys", "", false,
		"Skip importing keys of the Application")
	ImportAppCmd.Flags().BoolVarP(&importAppUpdateApplication, "update", "", false,
		"Update the Application if it is already imported")
	_ = ImportAppCmd.MarkFlagRequired("file")
	_ = ImportAppCmd.MarkFlagRequired("environment")
}
