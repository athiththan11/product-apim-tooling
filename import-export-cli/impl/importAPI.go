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

package impl

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wso2/product-apim-tooling/import-export-cli/specs/params"

	"github.com/mitchellh/go-homedir"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	v2 "github.com/wso2/product-apim-tooling/import-export-cli/specs/v2"

	"github.com/Jeffail/gabs"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var (
	reApiName                    = regexp.MustCompile(`[~!@#;:%^*()+={}|\\<>"',&/$]`)
)

// extractAPIDefinition extracts API information from jsonContent
func extractAPIDefinition(jsonContent []byte) (*v2.APIDefinition, error) {
	api := &v2.APIDefinition{}
	err := json.Unmarshal(jsonContent, &api)
	if err != nil {
		return nil, err
	}

	return api, nil
}

// getAPIDefinition scans filePath and returns APIDefinition or an error
func getAPIDefinition(filePath string) (*v2.APIDefinition, []byte, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, nil, err
	}

	var buffer []byte
	if info.IsDir() {
		_, content, err := resolveYamlOrJson(path.Join(filePath, "Meta-information", "api"))
		if err != nil {
			return nil, nil, err
		}
		buffer = content
	} else {
		return nil, nil, fmt.Errorf("looking for directory, found %s", info.Name())
	}
	api, err := extractAPIDefinition(buffer)
	if err != nil {
		return nil, nil, err
	}
	return api, buffer, nil
}

// mergeAPI merges environmentParams to the API given in apiDirectory
// for now only Endpoints are merged
func mergeAPI(apiDirectory string, environmentParams *params.Environment) error {
	// read api from Meta-information
	apiPath := filepath.Join(apiDirectory, "Meta-information", "api")
	utils.Logln(utils.LogPrefixInfo + "Reading API definition: ")
	fp, jsonContent, err := resolveYamlOrJson(apiPath)
	if err != nil {
		return err
	}
	utils.Logln(utils.LogPrefixInfo+"Loaded definition from:", fp)
	api, err := gabs.ParseJSON(jsonContent)
	if err != nil {
		return err
	}
	// extract environmentParams from file
	apiEndpointData, err := params.ExtractAPIEndpointConfig(api.Bytes())
	if err != nil {
		return err
	}

	configData, err := json.Marshal(environmentParams.Endpoints)
	if err != nil {
		return err
	}

	mergedAPIEndpoints, err := utils.MergeJSON([]byte(apiEndpointData), configData)
	if err != nil {
		return err
	}

	utils.Logln(utils.LogPrefixInfo + "Merging API")
	// replace original endpointConfig with merged version
	if _, err := api.SetP(string(mergedAPIEndpoints), "endpointConfig"); err != nil {
		return err
	}

	// replace original GatewayEnvironments only if they are present in api-params file
	if environmentParams.GatewayEnvironments != nil {
		if _, err := api.SetP(environmentParams.GatewayEnvironments, "environments"); err != nil {
			return err
		}
	}

	// Handle security parameters in api_params.yaml
	err = handleSecurityEndpointsParams(environmentParams.Security, api)
	if err != nil {
		return err
	}

	apiPath = filepath.Join(apiDirectory, "Meta-information", "api.yaml")
	utils.Logln(utils.LogPrefixInfo+"Writing merged API to:", apiPath)
	// write this to disk
	content, err := utils.JsonToYaml(api.Bytes())
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(apiPath, content, 0644)
	if err != nil {
		return err
	}
	return nil
}

// Handle security parameters in api_params.yaml
// @param envSecurityEndpointParams : Environment security endpoint parameters from api_params.yaml
// @param api : Parameters from api.yaml
// @return error
func handleSecurityEndpointsParams(envSecurityEndpointParams *params.SecurityData, api *gabs.Container) error {
	// If the user has set (either true or false) the enabled field under security in api_params.yaml, the
	// following code should be executed. (if not set, the security endpoint settings will be made
	// according to the api.yaml file as usually)
	// In Go, irrespective of whether a boolean value is "" or false, it will contain false by default.
	// That is why, here a string comparison was  made since strings can have both "" and "false"
	if envSecurityEndpointParams != nil && envSecurityEndpointParams.Enabled != "" {
		// Convert the string enabled to boolean
		boolEnabled, err := strconv.ParseBool(envSecurityEndpointParams.Enabled)
		if err != nil {
			return err
		}
		if _, err := api.SetP(boolEnabled, "endpointSecured"); err != nil {
			return err
		}
		// If endpoint security is enabled
		if boolEnabled {
			// Set the security endpoint parameters when the enabled field is set to true
			err := setSecurityEndpointsParams(envSecurityEndpointParams, api)
			if err != nil {
				return err
			}
		} else {
			// If endpoint security is not enabled, the username and password should be empty.
			// (otherwise the security will be enabled after importing the API, considering there are values
			// for username and passwords)
			if _, err := api.SetP("", "endpointUTUsername"); err != nil {
				return err
			}
			if _, err := api.SetP("", "endpointUTPassword"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Set the security endpoint parameters when the enabled field is set to true
// @param envSecurityEndpointParams : Environment security endpoint parameters from api_params.yaml
// @param api : Parameters from api.yaml
// @return error
func setSecurityEndpointsParams(envSecurityEndpointParams *params.SecurityData, api *gabs.Container) error {
	// Check whether the username, password and type fields have set in api_params.yaml
	if envSecurityEndpointParams.Username == "" {
		return errors.New("You have enabled endpoint security but the username is not found in the api_params.yaml")
	} else if envSecurityEndpointParams.Password == "" {
		return errors.New("You have enabled endpoint security but the password is not found in the api_params.yaml")
	} else if envSecurityEndpointParams.Type == "" {
		return errors.New("You have enabled endpoint security but the type is not found in the api_params.yaml")
	} else {
		// Override the username in api.yaml with the value in api_params.yaml
		if _, err := api.SetP(envSecurityEndpointParams.Username, "endpointUTUsername"); err != nil {
			return err
		}
		// Override the password in api.yaml with the value in api_params.yaml
		if _, err := api.SetP(envSecurityEndpointParams.Password, "endpointUTPassword"); err != nil {
			return err
		}
		// Set the fields in api.yaml according to the type field in api_params.yaml
		err := setEndpointSecurityType(envSecurityEndpointParams, api)
		if err != nil {
			return err
		}
	}
	return nil
}

// Set the fields in api.yaml according to the type field in api_params.yaml
// @param envSecurityEndpointParams : Environment security endpoint parameters from api_params.yaml
// @param api : Parameters from api.yaml
// @return error
func setEndpointSecurityType(envSecurityEndpointParams *params.SecurityData, api *gabs.Container) error {
	// Check whether the type is either basic or digest
	if envSecurityEndpointParams.Type == "digest" {
		if _, err := api.SetP(true, "endpointAuthDigest"); err != nil {
			return err
		}
	} else if envSecurityEndpointParams.Type == "basic" {
		if _, err := api.SetP(false, "endpointAuthDigest"); err != nil {
			return err
		}
	} else {
		// If the type is not either basic or digest, return an error
		return errors.New("Invalid endpoint security type found in the api_params.yaml. Should be either basic or digest")
	}
	return nil
}

// resolveImportFilePath resolves the archive/directory for import
// First will resolve in given path, if not found will try to load from exported directory
func resolveImportFilePath(file, defaultExportDirectory string) (string, error) {
	// check current path
	utils.Logln(utils.LogPrefixInfo + "Resolving for API path...")
	if _, err := os.Stat(file); os.IsNotExist(err) {
		// if the file not in given path it might be inside exported directory
		utils.Logln(utils.LogPrefixInfo+"Looking for API in", defaultExportDirectory)
		file = filepath.Join(defaultExportDirectory, file)
		if _, err := os.Stat(file); os.IsNotExist(err) {
			return "", err
		}
	}
	absPath, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}

	return absPath, nil
}

// extractArchive extracts the API and give the path.
// In API Manager archive there is a directory in the root which contains the API
// this function returns it appended to the destination path
func extractArchive(src, dest string) (string, error) {
	files, err := utils.Unzip(src, dest)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("invalid API archive")
	}
	r := strings.TrimPrefix(files[0], src)
	return filepath.Join(dest, strings.Split(filepath.Clean(r), string(os.PathSeparator))[0]), nil
}

// resolveAPIParamsPath resolves api_params.yaml path
// First it will look at AbsolutePath of the import path (the last directory)
// If not found it will look at current working directory
// If a path is provided search ends looking up at that path
func resolveAPIParamsPath(importPath, paramPath string) (string, error) {
	utils.Logln(utils.LogPrefixInfo + "Scanning for parameters file")
	if paramPath == utils.ParamFileAPI {
		// look in importpath
		if stat, err := os.Stat(importPath); err == nil && stat.IsDir() {
			loc := filepath.Join(importPath, utils.ParamFileAPI)
			utils.Logln(utils.LogPrefixInfo+"Scanning for", loc)
			if info, err := os.Stat(loc); err == nil && !info.IsDir() {
				// found api_params.yml in the importpath
				return loc, nil
			}
		}

		// look in the basepath of importPath
		base := filepath.Dir(importPath)
		fp := filepath.Join(base, utils.ParamFileAPI)
		utils.Logln(utils.LogPrefixInfo+"Scanning for", fp)
		if info, err := os.Stat(fp); err == nil && !info.IsDir() {
			// found api_params.yml in the base path
			return fp, nil
		}

		// look in the current working directory
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		utils.Logln(utils.LogPrefixInfo+"Scanning for", wd)
		fp = filepath.Join(wd, utils.ParamFileAPI)
		if info, err := os.Stat(fp); err == nil && !info.IsDir() {
			// found api_params.yml in the cwd
			return fp, nil
		}

		// no luck, it means paramPath is missing
		return "", fmt.Errorf("could not find %s. Please check %s exists in basepath of "+
			"import location or current working directory", utils.ParamFileAPI, utils.ParamFileAPI)
	} else {
		if info, err := os.Stat(paramPath); err == nil && !info.IsDir() {
			return paramPath, nil
		}
		return "", fmt.Errorf("could not find %s", paramPath)
	}
}

func getTempApiDirectory(file string) (string, error) {
	fileIsDir := false
	// create a temp directory
	tmpDir, err := ioutil.TempDir("", "apim")
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}

	if info, err := os.Stat(file); err == nil {
		fileIsDir = info.IsDir()
	} else {
		return "", err
	}
	if fileIsDir {
		// copy dir to a temp location
		utils.Logln(utils.LogPrefixInfo+"Copying from", file, "to", tmpDir)
		dest := filepath.Join(tmpDir, filepath.Base(file))
		err = utils.CopyDir(file, dest)
		if err != nil {
			return "", err
		}
		return dest, nil
	} else {
		// try to extract archive
		utils.Logln(utils.LogPrefixInfo+"Extracting", file, "to", tmpDir)
		finalPath, err := extractArchive(file, tmpDir)
		if err != nil {
			return "", err
		}
		return finalPath, nil
	}
}

// resolveYamlOrJson for a given filepath.
// first it will look for the yaml file, if not will fallback for json
// give filename without extension so resolver will resolve for file
// fn is resolved filename, jsonContent is file as a json object, error if anything wrong happen(or both files does not exists)
func resolveYamlOrJson(filename string) (string, []byte, error) {
	// lookup for yaml
	yamlFp := filename + ".yaml"
	if info, err := os.Stat(yamlFp); err == nil && !info.IsDir() {
		utils.Logln(utils.LogPrefixInfo+"Loading", yamlFp)
		// read it
		fn := yamlFp
		yamlContent, err := ioutil.ReadFile(fn)
		if err != nil {
			return "", nil, err
		}
		// load it as yaml
		jsonContent, err := utils.YamlToJson(yamlContent)
		if err != nil {
			return "", nil, err
		}
		return fn, jsonContent, nil
	}

	jsonFp := filename + ".json"
	if info, err := os.Stat(jsonFp); err == nil && !info.IsDir() {
		utils.Logln(utils.LogPrefixInfo+"Loading", jsonFp)
		// read it
		fn := jsonFp
		jsonContent, err := ioutil.ReadFile(fn)
		if err != nil {
			return "", nil, err
		}
		return fn, jsonContent, nil
	}

	return "", nil, fmt.Errorf("%s was not found as a YAML or JSON", filename)
}

func resolveCertPath(importPath, p string) (string, error) {
	// look in importPath
	utils.Logln(utils.LogPrefixInfo+"Resolving for", p)
	certfile := filepath.Join(importPath, p)
	utils.Logln(utils.LogPrefixInfo + "Looking in project directory")
	if info, err := os.Stat(certfile); err == nil && !info.IsDir() {
		// found file return it
		return certfile, nil
	}

	utils.Logln(utils.LogPrefixInfo + "Looking for absolute path")
	// try for an absolute path
	p, err := homedir.Expand(filepath.Clean(p))
	if err != nil {
		return "", err
	}

	if p != "" {
		return p, nil
	}

	return "", fmt.Errorf("%s not found", p)
}

// generateCertificates for the API
func generateCertificates(importPath string, environment *params.Environment) error {
	var certs []params.Cert

	if len(environment.Certs) == 0 {
		return nil
	}

	for _, cert := range environment.Certs {
		// read cert
		p, err := resolveCertPath(importPath, cert.Path)
		if err != nil {
			return err
		}
		pubPEMData, err := ioutil.ReadFile(p)
		if err != nil {
			return err
		}
		// get cert
		block, _ := pem.Decode(pubPEMData)
		enc := credentials.Base64Encode(string(block.Bytes))
		cert.Certificate = enc
		certs = append(certs, cert)
	}

	data, err := json.Marshal(certs)
	if err != nil {
		return err
	}

	yamlContent, err := utils.JsonToYaml(data)
	if err != nil {
		return err
	}

	// filepath to save certs
	fp := filepath.Join(importPath, "Meta-information", "endpoint_certificates.yaml")
	utils.Logln(utils.LogPrefixInfo+"Writing", fp)
	err = ioutil.WriteFile(fp, yamlContent, os.ModePerm)

	return err
}

// injectParamsToAPI injects ApiParams to API located in importPath using importEnvironment and returns the path to
// injected API location
func injectParamsToAPI(importPath, paramsPath, importEnvironment string) error {
	utils.Logln(utils.LogPrefixInfo+"Loading parameters from", paramsPath)
	apiParams, err := params.LoadApiParamsFromFile(paramsPath)
	if err != nil {
		return err
	}
	// check whether import environment is included in api configuration
	envParams := apiParams.GetEnv(importEnvironment)
	if envParams == nil {
		fmt.Println("Using default values as the environment is not present in api_param.yaml file")
	} else {
		//If environment parameters are present in parameter file
		err = mergeAPI(importPath, envParams)
		if err != nil {
			return err
		}

		err = generateCertificates(importPath, envParams)
		if err != nil {
			return err
		}
	}

	return nil
}

// getApiID returns id of the API by using apiInfo which contains name and version as info
func getApiID(accessOAuthToken, environment, name, version string) (string, error) {
	apiQuery := fmt.Sprintf("name:%s version:%s", name, version)
	count, apis, err := GetAPIListFromEnv(accessOAuthToken, environment, url.QueryEscape(apiQuery), "")
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	return apis[0].ID, nil
}

// isEmpty returns true when a given string is empty
func isEmpty(s string) bool {
	return len(strings.TrimSpace(s)) == 0
}

func preProcessAPI(apiDirectory string) error {
	dirty := false
	apiPath, jsonData, err := resolveYamlOrJson(filepath.Join(apiDirectory, "Meta-information", "api"))
	if err != nil {
		return err
	}
	utils.Logln(utils.LogPrefixInfo+"Loading API definition from: ", apiPath)

	api, err := gabs.ParseJSON(jsonData)
	if err != nil {
		return err
	}

	// preprocess endpoint config
	if !api.Exists("endpointConfig") {
		dirty = true
		conf, err := gabs.ParseJSON([]byte(`{"endpoint_type":"http"}`))
		if err != nil {
			return err
		}

		if api.Exists("productionUrl") {
			_, err = conf.SetP(api.Path("productionUrl").Data(), "production_endpoints.url")
			if err != nil {
				return err
			}
			_, err = conf.SetP("null", "production_endpoints.config")
			if err != nil {
				return err
			}
			_ = api.Delete("productionUrl")
		}
		if api.Exists("sandboxUrl") {
			_, err = conf.SetP(api.Path("sandboxUrl").Data(), "sandbox_endpoints.url")
			if err != nil {
				return err
			}
			_, err = conf.SetP("null", "sandbox_endpoints.config")
			if err != nil {
				return err
			}
			_ = api.Delete("sandboxUrl")
		}

		_, err = api.SetP(conf.String(), "endpointConfig")
		if err != nil {
			return err
		}
	}

	if dirty {
		yamlApiPath := filepath.Join(apiDirectory, "Meta-information", "api.yaml")
		utils.Logln(utils.LogPrefixInfo+"Writing preprocessed API to:", yamlApiPath)
		content, err := utils.JsonToYaml(api.Bytes())
		if err != nil {
			return err
		}
		// write this to disk
		err = ioutil.WriteFile(yamlApiPath, content, 0644)
		if err != nil {
			return err
		}
	}

	return nil
}

// Substitutes environment variables in the project files.
func replaceEnvVariables(apiFilePath string) error {
	for _, replacePath := range utils.EnvReplaceFilePaths {
		absFile := filepath.Join(apiFilePath, replacePath)
		// check if the path exists. If exists, proceed with processing. Otherwise, continue with the next items
		if fi, err := os.Stat(absFile); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else {
			switch mode := fi.Mode(); {
			case mode.IsDir():
				utils.Logln(utils.LogPrefixInfo+"Substituting env variables of files in folder path: ", absFile)
				err = utils.EnvSubstituteInFolder(absFile)
			case mode.IsRegular():
				utils.Logln(utils.LogPrefixInfo+"Substituting env of file: ", absFile)
				err = utils.EnvSubstituteInFile(absFile)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func populateApiWithDefaults(def *v2.APIDefinition) (dirty bool) {
	dirty = false
	if def.ContextTemplate == "" {
		if !strings.Contains(def.Context, "{version}") {
			def.ContextTemplate = path.Clean(def.Context + "/{version}")
			def.Context = strings.ReplaceAll(def.ContextTemplate, "{version}", def.ID.Version)
		} else {
			def.Context = path.Clean(def.Context)
			def.ContextTemplate = def.Context
			def.Context = strings.ReplaceAll(def.Context, "{version}", def.ID.Version)
		}
		dirty = true
	}
	if def.Tags == nil {
		def.Tags = []string{}
		dirty = true
	}
	if def.URITemplates == nil {
		def.URITemplates = []v2.URITemplates{}
		dirty = true
	}
	if def.Implementation == "" {
		def.Implementation = "ENDPOINT"
		dirty = true
	}
	return
}

// validateApiDefinition validates an API against basic rules
func validateApiDefinition(def *v2.APIDefinition) error {
	utils.Logln(utils.LogPrefixInfo + "Validating API")
	if isEmpty(def.ID.APIName) {
		return errors.New("apiName is required")
	}
	if reApiName.MatchString(def.ID.APIName) {
		return errors.New(`apiName contains one or more illegal characters (~!@#;:%^*()+={}|\\<>"',&\/$)`)
	}

	if isEmpty(def.ID.Version) {
		return errors.New("version is required")
	}
	if isEmpty(def.Context) {
		return errors.New("context is required")
	}
	if isEmpty(def.ContextTemplate) {
		return errors.New("contextTemplate is required")
	}
	if !strings.HasPrefix(def.Context, "/") {
		return errors.New("context should begin with a /")
	}
	if !strings.HasPrefix(def.ContextTemplate, "/") {
		return errors.New("contextTemplate should begin with a /")
	}
	return nil
}

// newFileUploadRequest forms an HTTP request
// Helper function for forming multi-part form data
// Returns the formed http request and errors
func newFileUploadRequest(uri string, method string, params map[string]string, paramName, path,
	accessToken string) (*http.Request, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(paramName, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(part, file)

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequest(method, uri, body)
	if err != nil {
		return nil, err
	}
	request.Header.Add(utils.HeaderAuthorization, utils.HeaderValueAuthBearerPrefix+" "+accessToken)
	request.Header.Add(utils.HeaderContentType, writer.FormDataContentType())
	request.Header.Add(utils.HeaderAccept, "*/*")
	request.Header.Add(utils.HeaderConnection, utils.HeaderValueKeepAlive)

	return request, err
}

// importAPI imports an API to the API manager
func importAPI(endpoint, httpMethod, filePath, accessToken string, extraParams map[string]string) error {
	req, err := newFileUploadRequest(endpoint, httpMethod, extraParams, "file",
		filePath, accessToken)
	if err != nil {
		return err
	}

	var tr *http.Transport
	if utils.Insecure {
		tr = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	} else {
		tr = &http.Transport{
			TLSClientConfig: utils.GetTlsConfigWithCertificate(),
		}
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   time.Duration(utils.HttpRequestTimeout) * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		utils.Logln(utils.LogPrefixError, err)
		return err
	}

	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		// 201 Created or 200 OK
		_ = resp.Body.Close()
		fmt.Println("Successfully imported API")
		return nil
	} else {
		// We have an HTTP error
		fmt.Println("Error importing API.")
		fmt.Println("Status: " + resp.Status)

		bodyBuf, err := ioutil.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}

		strBody := string(bodyBuf)
		fmt.Println("Response:", strBody)

		return errors.New(resp.Status)
	}
}

// ImportAPIToEnv function is used with import-api command
func ImportAPIToEnv(accessOAuthToken, importEnvironment, importPath, apiParamsPath string, importAPIUpdate, preserveProvider,
	importAPISkipCleanup bool) error {
	adminEndpoint := utils.GetAdminEndpointOfEnv(importEnvironment, utils.MainConfigFilePath)
	return ImportAPI(accessOAuthToken, adminEndpoint, importEnvironment, importPath, apiParamsPath, importAPIUpdate,
		preserveProvider, importAPISkipCleanup)
}

// ImportAPI function is used with import-api command
func ImportAPI(accessOAuthToken, adminEndpoint, importEnvironment, importPath, apiParamsPath string, importAPIUpdate, preserveProvider,
		importAPISkipCleanup bool) error {
	exportDirectory := filepath.Join(utils.ExportDirectory, utils.ExportedApisDirName)
	resolvedApiFilePath, err := resolveImportFilePath(importPath, exportDirectory)
	if err != nil {
		return err
	}
	utils.Logln(utils.LogPrefixInfo+"API Location:", resolvedApiFilePath)

	utils.Logln(utils.LogPrefixInfo + "Creating workspace")
	tmpPath, err := getTempApiDirectory(resolvedApiFilePath)
	if err != nil {
		return err
	}
	defer func() {
		if importAPISkipCleanup {
			utils.Logln(utils.LogPrefixInfo+"Leaving", tmpPath)
			return
		}
		utils.Logln(utils.LogPrefixInfo+"Deleting", tmpPath)
		err := os.RemoveAll(tmpPath)
		if err != nil {
			utils.Logln(utils.LogPrefixError + err.Error())
		}
	}()
	apiFilePath := tmpPath

	utils.Logln(utils.LogPrefixInfo + "Substituting environment variables in API files...")
	err = replaceEnvVariables(apiFilePath)
	if err != nil {
		return err
	}

	utils.Logln(utils.LogPrefixInfo + "Pre Processing API...")
	err = preProcessAPI(apiFilePath)
	if err != nil {
		return err
	}

	utils.Logln(utils.LogPrefixInfo + "Attempting to inject parameters to the API from api_params.yaml (if exists)")
	paramsPath, err := resolveAPIParamsPath(resolvedApiFilePath, apiParamsPath)
	if err != nil && apiParamsPath != utils.ParamFileAPI && apiParamsPath != "" {
		return err
	}
	if paramsPath != "" {
		//Reading API params file and populate api.yaml
		err := injectParamsToAPI(apiFilePath, paramsPath, importEnvironment)
		if err != nil {
			return err
		}
	}

	// Get API info
	apiInfo, originalContent, err := getAPIDefinition(apiFilePath)
	if err != nil {
		return err
	}
	// Fill with defaults
	if populateApiWithDefaults(apiInfo) {
		utils.Logln(utils.LogPrefixInfo + "API is populated with defaults")
		// api is dirty, write it to disk
		buf, err := json.Marshal(apiInfo)
		if err != nil {
			return err
		}

		newContent, err := gabs.ParseJSON(buf)
		if err != nil {
			return err
		}
		originalContent, err := gabs.ParseJSON(originalContent)
		if err != nil {
			return err
		}
		result, err := utils.MergeJSON(originalContent.Bytes(), newContent.Bytes())
		if err != nil {
			return err
		}

		yamlContent, err := utils.JsonToYaml(result)
		if err != nil {
			return err
		}
		p := filepath.Join(apiFilePath, "Meta-information", "api.yaml")
		utils.Logln(utils.LogPrefixInfo+"Writing", p)

		err = ioutil.WriteFile(p, yamlContent, 0644)
		if err != nil {
			return err
		}
	}
	// validate definition
	if err = validateApiDefinition(apiInfo); err != nil {
		return err
	}

	// if apiFilePath contains a directory, zip it
	if info, err := os.Stat(apiFilePath); err == nil && info.IsDir() {
		tmp, err := ioutil.TempFile("", "api-artifact*.zip")
		if err != nil {
			return err
		}
		utils.Logln(utils.LogPrefixInfo+"Creating API artifact", tmp.Name())
		err = utils.Zip(apiFilePath, tmp.Name())
		if err != nil {
			return err
		}
		defer func() {
			if importAPISkipCleanup {
				utils.Logln(utils.LogPrefixInfo+"Leaving", tmp.Name())
				return
			}
			utils.Logln(utils.LogPrefixInfo+"Deleting", tmp.Name())
			err := os.Remove(tmp.Name())
			if err != nil {
				utils.Logln(utils.LogPrefixError + err.Error())
			}
		}()
		apiFilePath = tmp.Name()
	}

	updateAPI := false
	if importAPIUpdate {
		// check for API existence
		id, err := getApiID(accessOAuthToken, importEnvironment, apiInfo.ID.APIName, apiInfo.ID.Version)
		if err != nil {
			return err
		}

		if id == "" {
			utils.Logln("The specified API was not found.")
			utils.Logln("Creating: %s %s\n", apiInfo.ID.APIName, apiInfo.ID.Version)
		} else {
			utils.Logln("Existing API found, attempting to update it...")
			utils.Logln("API ID:", id)
			updateAPI = true
		}
	}
	extraParams := map[string]string{}
	httpMethod := http.MethodPost
	adminEndpoint += "/import/api"
	if updateAPI {
		adminEndpoint += "?overwrite=" + strconv.FormatBool(true) + "&preserveProvider=" +
			strconv.FormatBool(preserveProvider)
	} else {
		adminEndpoint += "?preserveProvider=" + strconv.FormatBool(preserveProvider)
	}
	utils.Logln(utils.LogPrefixInfo + "Import URL: " + adminEndpoint)

	err = importAPI(adminEndpoint, httpMethod, apiFilePath, accessOAuthToken, extraParams)
	return err
}
