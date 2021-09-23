package config

import (
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/kelseyhightower/envconfig"
	log "github.com/sirupsen/logrus"
	"strings"
	"sync"
)

var (
	WHPB_USDT_PAIR = common.HexToAddress("0x0c85fe2dbc540386d2c1d907764956e18ea2ff6b")
	//USDT_Contract = common.HexToAddress("0xe78984541A634C52C760fbF97ca3f8E7d8f04C85")
	//WHPB_Contract = common.HexToAddress("0xBE05Ac1FB417c9EA435b37a9Cecd39Bc70359d31")
)

// Config  module config
type Config struct {
	Common
	DB
	Server
}

// Common 内部服务、外部服务公共参数
type Common struct {
	IP       string `json:"ip" envconfig:"IP" default:"0.0.0.0"`
	Port     int    `json:"port" envconfig:"PORT" default:"8867"`
	LogLevel string `json:"log_level" envconfig:"LOG_LEVEL" default:"info"`
	LogDir   string `json:"log_dir" envconfig:"LOG_DIR"`
	Mode     string `json:"mode" envconfig:"MODE"`
}

type DB struct {
	DBUserName           string `json:"db_user_name" envconfig:"DB_USER_NAME" default:"root"`
	DBUserPassword       string `json:"db_user_password" envconfig:"DB_USER_PASSWORD" required:"true" default:"tE88XcbarHjdmHpkDvgW"`
	DBIP                 string `json:"db_ip" envconfig:"DB_IP" required:"true" default:"127.0.0.1"`
	DBPort               int    `json:"db_port" envconfig:"DB_PORT" default:"3306"`
	DBName               string `json:"db_name" envconfig:"DB_NAME" default:"hpbswap_heco"`
	DBMaxAllowedPacket   int    `json:"db_max_allowed_packet" envconfig:"DB_MAX_ALLOWEDPACKET" default:"0"`
	DBSQLMaxOpenConns    int    `json:"dbsql_max_open_conns" envconfig:"DB_SQL_MAX_OPENCONNS" default:"50"`
	DBSQLMaxIdleConns    int    `json:"dbsql_max_idle_conns" envconfig:"DB_SQL_MAX_IDLECONNS" default:"25"`
	DBSQLConnMaxLifetime int64  `json:"dbsql_conn_max_lifetime" envconfig:"DB_SQL_CONNMAXLIFETIME" default:"3600"`
}

type Server struct {
	Web3Provider string `json:"web3_provider" envconfig:"WEB3_PROVIDER" required:"true" default:"https://http-testnet.hecochain.com"`

	FactoryContract string `json:"factory_contract" envconfig:"FACTORY_CONTRACT" required:"true"`
	RouterContract  string `json:"router_contract" envconfig:"ROUTER_CONTRACT" required:"true"`
	WHPBContract    string `json:"whpb_contract" envconfig:"WHPB_CONTRACT" default:"" `
	UsdtHpbContract string `json:"usdt_hpb_contract" envconfig:"USDT_HPB_CONTRACT" default:""`
	Pairs           string `json:"trade_competition_pairs" envconfig:"TRADE_COMPETITION_PAIRS" default:""`
}

var (
	confOnce sync.Once
	confLock sync.RWMutex
	gConf    = &Config{}
)

// Init inits global conifg
func Init() error {
	var err error

	confOnce.Do(func() {
		err = parseEnv()
		if err != nil {
			return
		}
	})

	return err
}

func parseEnv() error {
	if err := envconfig.Process("hpb", gConf); err != nil {
		log.WithError(err).Error("load config from env failed")
		return err
	}

	if gConf.DBUserPassword == "" {
		return errors.New("empty DBUserPassword")
	}

	if gConf.DBIP == "" {
		return errors.New("empty DBIP")
	}
	return nil

}

// GetConfig get config
func GetConfig() *Config {
	confLock.RLock()
	defer confLock.RUnlock()

	return gConf
}

func (c *Config) String() string {
	cj, _ := json.Marshal(c)
	return string(cj)
}

var ErrInvalidCMCTokenType = errors.New("Invalid CMCTokenType")

type CMCTokenTypeDecoder map[string][]string

func (c *CMCTokenTypeDecoder) Decode(value string) error {
	types := make(map[string][]string, 0)

	pairs := strings.Split(value, ",")
	if len(pairs) == 0 {
		return ErrInvalidCMCTokenType
	}
	for _, pair := range pairs {
		item := strings.Split(pair, ":")
		if len(item) != 2 {
			return ErrInvalidCMCTokenType
		}

		vs := strings.Split(item[1], "&")

		if len(vs) == 0 {
			return ErrInvalidCMCTokenType
		}

		types[item[0]] = vs
	}

	if len(types) == 0 {
		return ErrInvalidCMCTokenType
	}

	*c = types

	return nil
}

func (c *Config) GetInfuraAddress() string {
	return fmt.Sprintf("https://mainnet.infura.io/v3/%s", c.InfuraAPIKey)
}
