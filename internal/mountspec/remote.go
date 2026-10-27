package mountspec

// secretOnlyKeys never become backend options in the remoteType shorthand.
var secretOnlyKeys = map[string]bool{
	KeyConfigData: true, KeyConfigPassword: true, KeyRemoteType: true, KeyRemotePath: true,
}

// resolveRemote builds the isolated Remote from configData or the remoteType shorthand.
func resolveRemote(secrets, ctx map[string]string, volumeID string) (Remote, error) {
	path, err := remotePath(secrets, ctx)
	if err != nil {
		return Remote{}, err
	}
	configData, err := rawConfig(secrets, ctx[KeyRemote])
	if err != nil {
		return Remote{}, err
	}
	names, err := sectionNames(configData)
	if err != nil {
		return Remote{}, err
	}
	name, err := pickRemote(names, ctx[KeyRemote])
	if err != nil {
		return Remote{}, err
	}
	rename := make(map[string]string, len(names))
	for _, n := range names {
		rename[n] = isolatedName(n, volumeID)
	}
	return Remote{
		ConfigData: isolateConfig(configData, rename),
		FsPath:     rename[name] + ":" + path,
	}, nil
}

func remotePath(secrets, ctx map[string]string) (string, error) {
	fromSecret, inSecret := secrets[KeyRemotePath]
	fromCtx, inCtx := ctx[KeyRemotePath]
	if inSecret && inCtx {
		return "", newErr(KeyRemotePath, ReasonConflict, "set in both secret and volume context")
	}
	if inSecret {
		return fromSecret, nil
	}
	return fromCtx, nil
}

func rawConfig(secrets map[string]string, remote string) (string, error) {
	configData, hasConfig := secrets[KeyConfigData]
	remoteType, hasType := secrets[KeyRemoteType]
	backend := map[string]string{}
	for k, v := range secrets {
		if !secretOnlyKeys[k] {
			backend[k] = v
		}
	}
	switch {
	case hasConfig && hasType:
		return "", newErr(KeyRemoteType, ReasonConflict, "use either configData or remoteType")
	case hasConfig && len(backend) > 0:
		return "", newErr(KeyConfigData, ReasonConflict, "backend keys need remoteType, not configData")
	case hasConfig:
		return configData, nil
	case hasType && remoteType != "":
		if remote == "" {
			remote = shorthandRemoteName
		}
		return shorthandConfig(remote, remoteType, backend)
	}
	return "", newErr(KeyConfigData, ReasonMissing, "secret needs configData or remoteType")
}

func pickRemote(names []string, want string) (string, error) {
	if want == "" {
		if len(names) == 1 {
			return names[0], nil
		}
		return "", newErr(KeyRemote, ReasonMissing, "configData defines several remotes")
	}
	for _, n := range names {
		if n == want {
			return n, nil
		}
	}
	return "", newErr(KeyRemote, ReasonInvalidValue, "remote not defined in configData")
}
