package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

// `hubfly storage keys`: S3 access keys of a Storage project.

// storageMaxProjectEnv is the Dashboard's limit of variables per project.
const storageMaxProjectEnv = 50

func runStorageKeysGroup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return storageKeysListFlow(args)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return storageKeysListFlow(rest)
	case "create", "new":
		return storageKeysCreateFlow(rest)
	case "revoke", "delete", "rm":
		return storageKeysRevokeFlow(rest)
	case "help":
		printStorageUsage()
		return nil
	default:
		return fmt.Errorf("unknown storage keys command %q (run: hubfly storage help)", command)
	}
}

func storageKeysListFlow(args []string) error {
	const usage = "hubfly storage keys [list] [--project <p>] [--json]"
	fs := newStorageFlags("keys list")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[0], usage)
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	keys, err := fetchStorageAccessKeys(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	if *asJSON {
		if keys == nil {
			keys = []storageAccessKey{}
		}
		return rtPrintJSON(keys)
	}
	if len(keys) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No S3 access keys yet. Create one with: hubfly storage keys create <name> --project %s\n", target.project.ID)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ACCESS KEY\tNAME\tPERMISSION\tBUCKET\tSTATUS\tCREATED\tLAST USED")
	for _, key := range keys {
		status := "active"
		if key.RevokedAt != "" {
			status = "revoked"
		}
		bucket := key.BucketScope
		if bucket == "" {
			bucket = "all"
		}
		lastUsed := "never"
		if key.LastUsedAt != "" {
			lastUsed = storageTime(key.LastUsedAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", key.AccessKey, key.Name, key.Permission, bucket, status, storageTime(key.CreatedAt), lastUsed)
	}
	return tw.Flush()
}

// storageCredentialVariables are the variables `keys create` prints with
// --format dotenv/json and writes with --to (CLI-SPEC.md). Without an
// endpoint the endpoint variables are left out.
func storageCredentialVariables(endpoint string, credential storageCredential) []rtEnvVar {
	variables := []rtEnvVar{}
	if endpoint != "" {
		variables = append(variables, rtEnvVar{Key: "HUBFLY_STORAGE_ENDPOINT", Value: endpoint})
	}
	variables = append(variables,
		rtEnvVar{Key: "HUBFLY_STORAGE_REGION", Value: storageSigningRegion},
		rtEnvVar{Key: "HUBFLY_STORAGE_ACCESS_KEY_ID", Value: credential.AccessKey},
		rtEnvVar{Key: "HUBFLY_STORAGE_SECRET_ACCESS_KEY", Value: credential.SecretKey, Secret: true},
	)
	if credential.BucketScope != "" {
		variables = append(variables, rtEnvVar{Key: "HUBFLY_STORAGE_BUCKET", Value: credential.BucketScope})
	}
	if endpoint != "" {
		variables = append(variables, rtEnvVar{Key: "AWS_ENDPOINT_URL_S3", Value: endpoint})
	}
	variables = append(variables,
		rtEnvVar{Key: "AWS_REGION", Value: storageSigningRegion},
		rtEnvVar{Key: "AWS_ACCESS_KEY_ID", Value: credential.AccessKey},
		rtEnvVar{Key: "AWS_SECRET_ACCESS_KEY", Value: credential.SecretKey, Secret: true},
	)
	return variables
}

// resolveStorageEnvTarget finds the Cell project `keys create --to` writes
// into.
func resolveStorageEnvTarget(token, ref string) (project, error) {
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return project{}, err
	}
	var matches []project
	for _, item := range all {
		if item.ID == ref || strings.EqualFold(item.Name, ref) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return project{}, fmt.Errorf("project '%s' not found (list them with: hubfly projects)", ref)
	}
	if len(matches) > 1 {
		return project{}, fmt.Errorf("project name '%s' is ambiguous (%s); use the project id", ref, storageProjectList(matches))
	}
	if kind := matches[0].Type; kind != "" && kind != "cell" {
		return project{}, fmt.Errorf("--to needs a Cell project (where your app runs); '%s' is a %s project", matches[0].Name, kind)
	}
	return matches[0], nil
}

func storageKeysCreateFlow(args []string) error {
	const usage = "hubfly storage keys create <name> [--read-only] [--bucket <bucket>] [--format text|dotenv|json] [--to <cell-project>] [--project <p>]"
	fs := newStorageFlags("keys create")
	projectRef := storageProjectFlag(fs)
	readOnly := fs.Bool("read-only", false, "")
	bucketScope := fs.String("bucket", "", "")
	format := fs.String("format", "text", "")
	envTarget := fs.String("to", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	name := strings.TrimSpace(positionals[0])
	outputFormat := strings.ToLower(strings.TrimSpace(*format))
	if outputFormat == "env" {
		outputFormat = "dotenv"
	}
	if outputFormat != "text" && outputFormat != "dotenv" && outputFormat != "json" {
		return fmt.Errorf("invalid --format %q (expected text, dotenv or json)", *format)
	}
	scope := strings.TrimSpace(*bucketScope)
	if scope != "" {
		if err := validateStorageBucketName(scope); err != nil {
			return err
		}
	}
	permission := "readwrite"
	if *readOnly {
		permission = "read"
	}
	target, err := resolveStorageTarget(*projectRef, outputFormat == "text")
	if err != nil {
		return err
	}
	overview, err := fetchStorageOverview(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	endpoint := strings.TrimSpace(overview.S3Endpoint)

	// Everything --to needs is checked before the key exists, so a failure
	// does not leave an unused key behind.
	var envProject project
	var existing []ProjectEnvVar
	if ref := strings.TrimSpace(*envTarget); ref != "" {
		if endpoint == "" {
			return errors.New("this Storage region has no S3 endpoint configured yet, so the app variables would be incomplete; try again later")
		}
		if envProject, err = resolveStorageEnvTarget(target.token, ref); err != nil {
			return err
		}
		if existing, err = fetchProjectEnv(target.token, envProject.ID); err != nil {
			return fmt.Errorf("failed to read the environment of '%s': %w", envProject.Name, err)
		}
		names := map[string]bool{}
		for _, item := range existing {
			names[item.Key] = true
		}
		for _, variable := range storageCredentialVariables(endpoint, storageCredential{BucketScope: scope}) {
			names[variable.Key] = true
		}
		if len(names) > storageMaxProjectEnv {
			return fmt.Errorf("project '%s' would have %d environment variables; the limit is %d", envProject.Name, len(names), storageMaxProjectEnv)
		}
	}

	credential, err := createStorageAccessKey(target.token, target.project.ID, name, permission, scope)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("bucket '%s' not found", scope))
	}
	if credential.BucketScope == "" {
		credential.BucketScope = scope
	}
	variables := storageCredentialVariables(endpoint, credential)

	if envProject.ID != "" {
		return writeStorageCredentialEnv(target, envProject, existing, credential, variables, outputFormat == "json")
	}

	switch outputFormat {
	case "json":
		out := map[string]string{}
		for _, variable := range variables {
			out[variable.Key] = variable.Value
		}
		_, _ = fmt.Fprintln(rtErr, "Save the secret now: it is shown only once.")
		return rtPrintJSON(out)
	case "dotenv":
		_, _ = fmt.Fprintln(rtErr, "Save the secret now: it is shown only once.")
		_, _ = fmt.Fprintf(rtOut, "# HubFly Storage: key %s (%s) for project %s\n", credential.Name, credential.Permission, target.project.Name)
		for _, variable := range variables {
			_, _ = fmt.Fprintf(rtOut, "%s=%s\n", variable.Key, formatDotenvValue(variable.Value))
		}
		return nil
	}
	scopeLabel := "all buckets"
	if credential.BucketScope != "" {
		scopeLabel = "bucket " + credential.BucketScope
	}
	_, _ = fmt.Fprintf(rtOut, "Created S3 access key '%s' (%s, %s).\n", credential.Name, credential.Permission, scopeLabel)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Endpoint:\t%s\n", storageEndpointLabel(endpoint))
	_, _ = fmt.Fprintf(tw, "  Region:\t%s (any SigV4 region is accepted; use path-style addressing)\n", storageSigningRegion)
	_, _ = fmt.Fprintf(tw, "  Access key ID:\t%s\n", credential.AccessKey)
	_, _ = fmt.Fprintf(tw, "  Secret access key:\t%s\n", credential.SecretKey)
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtErr, "Save the secret now: it is shown only once. Revoke the key with: hubfly storage keys revoke "+credential.AccessKey)
	return nil
}

// writeStorageCredentialEnv merges the credential variables into a Cell
// project's environment and prints only the variable names. If the write
// fails the new key is revoked, because its secret cannot be shown again.
func writeStorageCredentialEnv(target storageTarget, envProject project, existing []ProjectEnvVar, credential storageCredential, variables []rtEnvVar, asJSON bool) error {
	merged := map[string]ProjectEnvVar{}
	for _, item := range existing {
		merged[item.Key] = item
	}
	replaced := []string{}
	names := make([]string, 0, len(variables))
	for _, variable := range variables {
		current, exists := merged[variable.Key]
		if exists && current.Value != variable.Value {
			replaced = append(replaced, variable.Key)
		}
		merged[variable.Key] = ProjectEnvVar{ID: current.ID, Key: variable.Key, Value: variable.Value, IsSecret: variable.Secret}
		names = append(names, variable.Key)
	}
	list := make([]ProjectEnvVar, 0, len(merged))
	for _, item := range merged {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	result, err := updateProjectEnv(target.token, envProject.ID, list)
	if err != nil {
		if revokeErr := revokeStorageAccessKey(target.token, target.project.ID, credential.AccessKey); revokeErr != nil {
			return fmt.Errorf("failed to write the variables into '%s': %w\nThe new key %s could not be revoked either (%v); revoke it with: hubfly storage keys revoke %s --project %s",
				envProject.Name, err, credential.AccessKey, revokeErr, credential.AccessKey, target.project.ID)
		}
		return fmt.Errorf("failed to write the variables into '%s': %w\nThe new key was revoked; run the command again", envProject.Name, err)
	}
	if asJSON {
		return rtPrintJSON(map[string]any{
			"accessKey":   credential.AccessKey,
			"name":        credential.Name,
			"permission":  credential.Permission,
			"bucketScope": credential.BucketScope,
			"project":     map[string]string{"id": envProject.ID, "name": envProject.Name},
			"variables":   names,
			"replaced":    replaced,
			"redeployed":  result.Redeployed,
		})
	}
	_, _ = fmt.Fprintf(rtOut, "Created S3 access key '%s' (%s) and wrote %d variables into '%s':\n", credential.Name, credential.AccessKey, len(names), envProject.Name)
	_, _ = fmt.Fprintf(rtOut, "  %s\n", strings.Join(names, ", "))
	if len(replaced) > 0 {
		_, _ = fmt.Fprintf(rtOut, "Replaced existing values of: %s\n", strings.Join(replaced, ", "))
	}
	_, _ = fmt.Fprintf(rtOut, "Secrets are stored as secret variables; %d container(s) reloaded.\n", result.Redeployed)
	return nil
}

func storageKeysRevokeFlow(args []string) error {
	const usage = "hubfly storage keys revoke <accessKey> [--yes] [--project <p>]"
	fs := newStorageFlags("keys revoke")
	projectRef := storageProjectFlag(fs)
	yes := storageYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	accessKey := strings.TrimSpace(positionals[0])
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	keys, err := fetchStorageAccessKeys(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	var found *storageAccessKey
	for index := range keys {
		if keys[index].AccessKey == accessKey {
			found = &keys[index]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("access key '%s' not found in '%s' (list them with: hubfly storage keys --project %s)", accessKey, target.project.Name, target.project.ID)
	}
	if found.RevokedAt != "" {
		_, _ = fmt.Fprintf(rtOut, "Access key %s is already revoked.\n", accessKey)
		return nil
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Revoke access key %s ('%s')? Apps using it lose access immediately.", accessKey, found.Name), "revoke it")
	if err != nil || !confirmed {
		return err
	}
	if err := revokeStorageAccessKey(target.token, target.project.ID, accessKey); err != nil {
		return storageAPIError(err, fmt.Sprintf("access key '%s' not found", accessKey))
	}
	_, _ = fmt.Fprintf(rtOut, "Revoked access key %s.\n", accessKey)
	return nil
}
