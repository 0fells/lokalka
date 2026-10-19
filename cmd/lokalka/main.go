package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"lokalka/internal/admin"
	"lokalka/internal/stamp"
	"lokalka/internal/store"
	"lokalka/internal/vpn"
)

var (
	defaultServer   = ""
	defaultNet      = ""
	defaultPassword = ""
	defaultMode     = ""
)

func main() {
	log.SetFlags(log.Ltime)
	file, hasFile, terr := ownConfig()
	if terr != nil {
		showError(terr)
		os.Exit(1)
	}
	ensureAdmin()
	bare := len(os.Args) < 2
	var err error
	if hasFile && file.Role == stamp.RolePanel {
		err = runPanelGUI(file)
	} else if hasFile {
		err = runJoinStamp(file)
	} else if bare && defaultMode == "admin" {
		err = runAdminGUI()
	} else if len(os.Args) >= 2 && looksLikeConfig(os.Args[1]) {
		err = runJoinFile(os.Args[1], os.Args[2:])
	} else if bare {
		var path string
		path, err = sidecarConfig()
		if err == nil && path != "" {
			err = runJoinFile(path, nil)
		} else if err == nil && (defaultServer == "" || defaultNet == "") {
			err = errors.New("Это Lokalka без файла входа. Попросите у администратора сети свой exe или положите рядом файл .lokalka.")
		} else if err == nil {
			joinArgs := []string{"--server", defaultServer, "--net", defaultNet}
			if defaultPassword != "" {
				joinArgs = append(joinArgs, "--password", defaultPassword)
			}
			err = runJoin(joinArgs)
		}
	} else {
		switch os.Args[1] {
		case "server":
			err = runServer(os.Args[2:])
		case "key":
			err = runKey(os.Args[2:])
		case "join":
			err = runJoin(os.Args[2:])
		case "net":
			err = runNet(os.Args[2:])
		case "help", "-h", "--help":
			usage()
			return
		default:
			usage()
			os.Exit(2)
		}
	}
	if err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) && !errors.Is(err, errShownRunning) {
		showError(err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Lokalka: локальная сеть для игр с друзьями через свой сервер.

  lokalka server [--listen 0.0.0.0:51820] [--public ХОСТ:ПОРТ] [--admin 127.0.0.1:8756] [--data data/state.json]
                 пароль админки: переменная LOKALKA_ADMIN_PASSWORD (или --admin-password)
  lokalka key [--data data/state.json]           ключ сервера, его сверяет админка при первом входе
  lokalka net create --name ИМЯ [--password ПАРОЛЬ] [--admin http://127.0.0.1:8756]
  lokalka net list [--admin http://127.0.0.1:8756]
  lokalka net delete --name ИМЯ [--admin http://127.0.0.1:8756]
  lokalka join --server ХОСТ:ПОРТ --net ИМЯ [--password ПАРОЛЬ] [--hostname ИМЯ] [--tun auto|none]

`)
	fmt.Fprintf(os.Stderr, "Файл входа (exe из админки) подключается сам, без аргументов.\n")
	fmt.Fprintf(os.Stderr, "Файл .lokalka из админки можно перетащить на программу или положить рядом с ней.\n")
	fmt.Fprintf(os.Stderr, "Пароль можно передать флагом, переменной LOKALKA_PASSWORD или ввести в консоли.\n")
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:51820", "адрес для клиентов, TCP и UDP")
	publicAddr := fs.String("public", "", "адрес в скачиваемых конфигах, host:port")
	adminAddr := fs.String("admin", "127.0.0.1:8756", "веб-админка, только localhost")
	adminPass := fs.String("admin-password", "", "пароль входа в админку из приложения (лучше LOKALKA_ADMIN_PASSWORD: аргументы видны в ps)")
	data := fs.String("data", "data/state.json", "файл с сетями и арендой адресов")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(*data)
	if err != nil {
		return err
	}

	if *adminPass == "" {
		*adminPass = os.Getenv("LOKALKA_ADMIN_PASSWORD")
	}
	if strings.TrimSpace(*adminPass) != "" {
		if err := st.SetAdminPassword(*adminPass); err != nil {
			return err
		}
	}
	srv := vpn.NewServer(st)
	if strings.TrimSpace(*publicAddr) != "" {
		endpoint, err := admin.NormalizeEndpoint(*publicAddr)
		if err != nil {
			return err
		}
		srv.SetPublic(endpoint)
	}
	if err := srv.Listen(*listen); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{Addr: *adminAddr, Handler: admin.Handler(srv)}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("админка: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shut)
	}()

	log.Printf("клиенты подключаются к %s (TCP и UDP)", srv.Addr())
	log.Printf("ключ сервера: %s", srv.ServerKey())
	log.Printf("админка: http://%s", *adminAddr)
	return srv.Serve(ctx)
}

func runKey(args []string) error {
	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	data := fs.String("data", "data/state.json", "файл с сетями и ключом сервера")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := store.ReadPublicKey(*data)
	if err != nil {
		return err
	}
	fmt.Println(vpn.FormatServerKey(key))
	return nil
}

func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	server := fs.String("server", "", "адрес сервера host:port")
	network := fs.String("net", "", "имя сети")
	pass := fs.String("password", "", "пароль сети")
	hostname := fs.String("hostname", "", "имя этого компьютера")
	tunMode := fs.String("tun", "auto", "auto или none")
	console := fs.Bool("console", false, "без окна, только консоль")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *network == "" {
		return errors.New("нужны флаги --server и --net")
	}
	useGUI := !*console && guiSupported() && *tunMode != "none"
	var password string
	var err error
	if *pass == "" && passwordEnv() == "" && useGUI {
		password = ""
	} else {
		password, err = readPassword(*pass)
		if err != nil {
			return err
		}
	}
	host := strings.TrimSpace(*hostname)
	if host == "" {
		host, _ = os.Hostname()
	}
	host = cleanHost(host)
	cfg := vpn.Config{
		ServerAddr: *server,
		Network:    *network,
		Password:   password,
		Hostname:   host,
	}
	switch *tunMode {
	case "auto":
	case "none":
		cfg.NoTUN = true
	default:
		return errors.New("--tun принимает auto или none")
	}
	return startClient(cfg, !*console)
}

func runJoinStamp(file stamp.File) error {
	key, err := vpn.ParseServerKey(file.ServerKey)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	cfg := vpn.Config{
		ServerAddr: file.Server,
		ServerKey:  key,
		Network:    file.Network,
		Password:   file.Password,
		Hostname:   cleanHost(host),
		ConfigID:   strings.TrimSpace(file.ConfigID),
	}
	return startClient(cfg, true)
}

func startClient(cfg vpn.Config, gui bool) error {
	if !cfg.NoTUN {
		release, err := lockSingleClient()
		if err != nil {
			return err
		}
		defer release()
	}
	if gui && guiSupported() && !cfg.NoTUN {
		return runGUI(cfg)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return vpn.New(cfg).Run(ctx)
}

func runJoinFile(path string, extra []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	hostname := fs.String("hostname", "", "имя этого компьютера")
	tunMode := fs.String("tun", "auto", "auto или none")
	console := fs.Bool("console", false, "без окна, только консоль")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(extra); err != nil {
		return err
	}
	cfg, err := loadClientFile(path)
	if err != nil {
		return err
	}
	if host := strings.TrimSpace(*hostname); host != "" {
		cfg.Hostname = cleanHost(host)
	}
	switch *tunMode {
	case "auto":
	case "none":
		cfg.NoTUN = true
	default:
		return errors.New("--tun принимает auto или none")
	}
	return startClient(cfg, !*console)
}

func runNet(args []string) error {
	if len(args) == 0 {
		return errors.New("команда: create, list или delete")
	}
	switch args[0] {
	case "create":
		return netCreate(args[1:])
	case "list":
		return netList(args[1:])
	case "delete":
		return netDelete(args[1:])
	default:
		return errors.New("команда: create, list или delete")
	}
}

func netCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	name := fs.String("name", "", "имя сети")
	pass := fs.String("password", "", "пароль")
	adminURL := fs.String("admin", "http://127.0.0.1:8756", "адрес админки")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("нужен флаг --name")
	}
	password, err := readPassword(*pass)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"name": *name, "password": password})
	res, err := http.Post(strings.TrimRight(*adminURL, "/")+"/api/networks", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("админка недоступна: %w", err)
	}
	defer res.Body.Close()
	return finishAPI(res)
}

func netList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	adminURL := fs.String("admin", "http://127.0.0.1:8756", "адрес админки")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := http.Get(strings.TrimRight(*adminURL, "/") + "/api/networks")
	if err != nil {
		return fmt.Errorf("админка недоступна: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return finishAPI(res)
	}
	var nets []admin.NetworkView
	if err := json.NewDecoder(res.Body).Decode(&nets); err != nil {
		return err
	}
	if len(nets) == 0 {
		fmt.Println("сетей нет")
		return nil
	}
	for _, n := range nets {
		fmt.Printf("%s  онлайн %d\n", n.Name, n.Online)
		for _, p := range n.Peers {
			fmt.Printf("  %s  %s\n", p.IP, p.Hostname)
		}
	}
	return nil
}

func netDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	name := fs.String("name", "", "имя сети")
	adminURL := fs.String("admin", "http://127.0.0.1:8756", "адрес админки")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("нужен флаг --name")
	}
	req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(*adminURL, "/")+"/api/networks/"+*name, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("админка недоступна: %w", err)
	}
	defer res.Body.Close()
	return finishAPI(res)
}

func finishAPI(res *http.Response) error {
	var body struct {
		Error string `json:"error"`
		OK    string `json:"ok"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode >= 300 {
		if body.Error != "" {
			return errors.New(body.Error)
		}
		return fmt.Errorf("админка вернула %s", res.Status)
	}
	if body.OK != "" {
		fmt.Println(body.OK)
	}
	return nil
}

func readPassword(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if env := passwordEnv(); env != "" {
		return env, nil
	}
	fmt.Fprint(os.Stderr, "Пароль: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("пустой пароль")
	}
	return line, nil
}

func passwordEnv() string {
	return os.Getenv("LOKALKA_PASSWORD")
}

func cleanHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return "pc"
	}
	var b strings.Builder
	changed := false
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			changed = true
			b.WriteString(translit(r))
		}
	}
	out := b.String()
	if !changed {
		if out == "" {
			return "pc"
		}
		if len(out) > 32 {
			out = out[:32]
		}
		return out
	}
	sum := sha256.Sum256([]byte(host))
	tail := hex.EncodeToString(sum[:2])
	out = strings.Trim(out, "-")
	if out == "" {
		out = "pc"
	}
	if len(out) > 32-len(tail)-1 {
		out = out[:32-len(tail)-1]
	}
	return out + "-" + tail
}

var translitTable = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya", 'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g", 'ў': "u",
}

func translit(r rune) string {
	if r == ' ' {
		return "-"
	}
	lower := unicode.ToLower(r)
	lat, ok := translitTable[lower]
	if !ok {
		return ""
	}
	if lower != r && lat != "" {
		return strings.ToUpper(lat[:1]) + lat[1:]
	}
	return lat
}
