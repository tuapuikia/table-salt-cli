package main

import (
    "fmt"
    "os"
    "log"
    "net"
    "strings"
    "bytes"
    "runtime"
    "bufio"
    "regexp"
    "path/filepath"
    "encoding/json"
    "golang.org/x/crypto/ssh"
    "golang.org/x/crypto/ssh/agent"
)

var saltCommand string
var configuration = Configuration{}
var bsshClientConnection *ssh.Client = nil
var hostKeyCallBackConfig ssh.HostKeyCallback = nil
var sshConfig *ssh.ClientConfig = nil

type Configuration struct {
    Auth string
    UseJump bool
    UseSudo bool
    SudoType string
    HostKeyCheck bool
    JumpUsername string
    JumpPassword string
    JumpPrivateKey string
    JumpServer string
    RemoteEndpoint string
    RemoteUsername string
    RemotePassword string
    RemotePrivateKey string
}

func SSHAgent() ssh.AuthMethod {
    if sshAgent, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
        return ssh.PublicKeysCallback(agent.NewClient(sshAgent).Signers)
    }
    return nil
}

func HostKeyCheck(remoteHost string) (ssh.HostKeyCallback) {

    host := remoteHost
    file, err := os.Open(filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"))
    if err != nil {
        log.Fatal(err)
        os.Exit(1)
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    var hostKey ssh.PublicKey
    for scanner.Scan() {
        fields := strings.Split(scanner.Text(), " ")
        if len(fields) != 3 {
            continue
        }
        if strings.Contains(fields[0], host) {
            var err error
            hostKey, _, _, _, err = ssh.ParseAuthorizedKey(scanner.Bytes())
            if err != nil {
                log.Fatalf("Error parsing %q: %v", fields[2], err)
            }
            break
        }
    }

    if hostKey == nil {
        log.Fatalf("No hostkey for %s. You can disable checks in the config by setting HostKeyCheck to false.", host)
        os.Exit(1)
    }

    return ssh.FixedHostKey(hostKey)

}

func setupJump() {

    err := error(nil)
    bsshAuthMethod := []ssh.AuthMethod{SSHAgent()}

    if configuration.Auth == "key" && len(configuration.JumpPrivateKey) > 0 {
        jumpKey, err := ssh.ParsePrivateKey([]byte(configuration.JumpPrivateKey))
        if err != nil {
            fmt.Println("Could not parse private key file. Check the path and ensure it is not encrypted.")
            os.Exit(1)
        }
        bsshAuthMethod[0] = ssh.PublicKeys(jumpKey)
    } else if configuration.Auth == "agent" && runtime.GOOS != "windows" {
        bsshAuthMethod[0] = SSHAgent()
    } else if configuration.Auth == "password" && len(configuration.JumpPassword) > 0 {
        bsshAuthMethod[0] = ssh.Password(configuration.JumpPassword)
    } else {
        fmt.Println("No supported authentication modes available/supported. Double check your configuration.")
        os.Exit(1)
    }

    if configuration.HostKeyCheck {
        hostSplit := strings.Split(configuration.JumpServer, ":")
        hostKeyCallBackConfig = HostKeyCheck(hostSplit[0])
    } else {
        hostKeyCallBackConfig = ssh.InsecureIgnoreHostKey()
    }

    bsshConfig := &ssh.ClientConfig{
        User: configuration.JumpUsername,
        Auth: bsshAuthMethod,
        HostKeyCallback: hostKeyCallBackConfig,
    }

    bsshClientConnection, err = ssh.Dial("tcp", configuration.JumpServer, bsshConfig)
    if err != nil {
        log.Fatal(err)
        os.Exit(1)
    }

}

func generateSaltCommand() (string) {

    args := os.Args[1:]

    for i := 0; i < len(args); i++ {
        args[i] = "\""+args[i]+"\""
    }

    saltCommand := "salt " + strings.Join(args, " ")

    // Handle sudo if necessary
    if configuration.UseSudo {
        if configuration.SudoType == "nopassword" {
            saltCommand = "sudo " + saltCommand
        } else {
            if len(configuration.RemotePassword) > 0 {
                saltCommand = "echo \"" + configuration.RemotePassword + "\" | sudo -kS " + saltCommand
            }
        }

    }

    return saltCommand

}

func useJump() (string) {

    var commandResult string

    jumpConnection, err := bsshClientConnection.Dial("tcp", configuration.RemoteEndpoint)
    if err != nil {
        log.Fatal(err)
        os.Exit(1)
    }

    ncc, chans, reqs, err := ssh.NewClientConn(jumpConnection, configuration.RemoteEndpoint, sshConfig)
    if err != nil {
        log.Fatal(err)
        os.Exit(1)
    }

    sshClientConnection := ssh.NewClient(ncc, chans, reqs)

    session, err := sshClientConnection.NewSession()
    if err != nil {
        fmt.Println(err.Error())
    }
    defer session.Close()

    if ! configuration.UseSudo {
        commandResult = executePtySession(session)
    } else {
        commandResult = executePtySession(session)
    }

    return commandResult

}

func goDirect() (string) {

    var commandResult string

    sshClientConnection, err := ssh.Dial("tcp", configuration.RemoteEndpoint, sshConfig)
    if err != nil {
        log.Fatal(err)
        os.Exit(1)
    }

    session, err := sshClientConnection.NewSession()
    if err != nil {
        fmt.Println(err.Error())
    }
    defer session.Close()

    if ! configuration.UseSudo {
        commandResult = executePtySession(session)
    } else {
        commandResult = executePtySession(session)
    }

    return commandResult

}

func executeNonPtySession(sshSession *ssh.Session) (string) {
        var b bytes.Buffer
        sshSession.Stdout = &b
        sshSession.Run(saltCommand)
        commandResult := b.String()
        return commandResult
}

func executePtySession(sshSession *ssh.Session) (string) {

    modes := ssh.TerminalModes{
        ssh.ECHO:  0,
        ssh.TTY_OP_ISPEED: 14400,
        ssh.TTY_OP_OSPEED: 14400,
        ssh.IGNCR: 1,
    }

    if err := sshSession.RequestPty("vt100", 80, 40, modes); err != nil {
        log.Fatalf("request for pseudo terminal failed: %s", err)
    }

    var b bytes.Buffer
    sshSession.Stdout = &b
    sshSession.Run(saltCommand)
    reg := regexp.MustCompile(`^.*: (.*)`)
    commandResult := reg.ReplaceAllString(b.String(), "${1}")

    return commandResult

}

func main() {

    // Open configuration JSON
    configPath := "ts_conf.json"
    if len(os.Getenv("TABLESALTCONF")) > 0 {
        configPath = os.Getenv("TABLESALTCONF")
    }
    file, _ := os.Open(configPath)
    decoder := json.NewDecoder(file)
    configuration = Configuration{}
    err := decoder.Decode(&configuration)
    if err != nil {
      fmt.Println("Error: Invalid or missing configuration. ", err)
    }

    // Parse salt command args
    saltCommand = generateSaltCommand()

    // Connect to bastion/jump server if necessary
    if configuration.UseJump {
        setupJump()
    }

    // Connect to salt-master directly or through jump server
    sshAuthMethod := []ssh.AuthMethod{SSHAgent()}

    if configuration.Auth == "key" && len(configuration.RemotePrivateKey) > 0 {
        remoteKey, err := ssh.ParsePrivateKey([]byte(configuration.RemotePrivateKey))
        if err != nil {
            fmt.Println("Could not parse private key file. Check the path and ensure it is not encrypted.")
            os.Exit(1)
        }
        sshAuthMethod[0] = ssh.PublicKeys(remoteKey)
    } else if configuration.Auth == "agent" && runtime.GOOS != "windows" {
        sshAuthMethod[0] = SSHAgent()
    } else if configuration.Auth == "password" && len(configuration.RemotePassword) > 0 {
        sshAuthMethod[0] = ssh.Password(configuration.RemotePassword)
    } else {
        fmt.Println("No supported authentication modes available/supported. Double check your configuration.")
        os.Exit(1)
    }

    if configuration.HostKeyCheck {
        hostSplit := strings.Split(configuration.RemoteEndpoint, ":")
        hostKeyCallBackConfig = HostKeyCheck(hostSplit[0])
    } else {
        hostKeyCallBackConfig = ssh.InsecureIgnoreHostKey()
    }

    sshConfig = &ssh.ClientConfig{
        User: configuration.RemoteUsername,
        Auth: sshAuthMethod,
        HostKeyCallback: hostKeyCallBackConfig,
    }

    // Execute salt command
    var saltOutput string
    if configuration.UseJump {
        saltOutput = useJump()
    } else {
        saltOutput = goDirect()
    }

    fmt.Println(saltOutput)

}