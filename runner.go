package runner

import (
	"BeeScan-scan/pkg/config"
	"BeeScan-scan/pkg/db"
	"BeeScan-scan/pkg/httpx"
	"BeeScan-scan/pkg/job"
	log2 "BeeScan-scan/pkg/log"
	"BeeScan-scan/pkg/result"
	"BeeScan-scan/pkg/scan/fringerprint"
	"BeeScan-scan/pkg/scan/getipbydomain"
	"BeeScan-scan/pkg/scan/gonmap"
	"BeeScan-scan/pkg/scan/httpcheck"
	"BeeScan-scan/pkg/scan/icmp"
	"BeeScan-scan/pkg/scan/ipinfo"
	"BeeScan-scan/pkg/scan/ping"
	"BeeScan-scan/pkg/scan/tcp"
	"BeeScan-scan/pkg/util"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	redis2 "github.com/go-redis/redis"
	"github.com/projectdiscovery/hmap/store/hybrid"
)

/*
程序功能：运行实例
*/

type Runner struct {
	Ip       string
	Port     string
	Domain   string
	Protocol string
	Ht       *httpx.HTTPX
	Hm       *hybrid.HybridMap
	Fofa     *fringerprint.FofaPrints
	Output   *result.Output
}

// NewRunner 创建runner实例
func NewRunner(ip string, port string, domain string, protocol string, FofaPrints *fringerprint.FofaPrints) (*Runner, error) {
	runner := &Runner{
		Ip:       ip,
		Port:     port,
		Domain:   domain,
		Protocol: protocol,
		Output:   &result.Output{},
	}
	if hm, err := hybrid.New(hybrid.DefaultDiskOptions); err != nil {
		runner.Hm = nil
	} else {
		runner.Hm = hm
	}

	// http
	HttpOptions := &httpx.HTTPOptions{
		Timeout:          3 * time.Second,
		RetryMax:         3,
		FollowRedirects:  true,
		Unsafe:           false,
		DefaultUserAgent: httpx.GetRadnomUserAgent(),
	}
	ht, err := httpx.NewHttpx(HttpOptions)
	if err != nil {
		return nil, err
	}
	runner.Ht = ht
	runner.Fofa = FofaPrints
	return runner, nil

}

// http请求
func (r *Runner) do(fullUrl string) (*httpx.Response, error) {
	req, err := r.Ht.NewRequest("GET", fullUrl)

	if err != nil {
		return &httpx.Response{}, err
	}
	resp, err2 := r.Ht.Do(req)
	return resp, err2
}

// Request 指纹识别
func (r *Runner) Request() result.FingerResult {
	var resp *httpx.Response
	var fullUrl string
	if r.Ht != nil && r.Hm != nil {
		if r.Ht.Dialer != nil {
			r.Close()
		}
	}
	log2.Info("[HttpRequest]:", r.Ip)

	retried := false
	protocol := httpx.HTTPS
retry:
	if r.Domain != "" && r.Port != "80" {
		fullUrl = fmt.Sprintf("%s://%s:%s", protocol, r.Domain, r.Port)
	} else if r.Port != "80" {
		fullUrl = fmt.Sprintf("%s://%s:%s", protocol, r.Ip, r.Port)
	} else {
		fullUrl = fmt.Sprintf("%s://%s", protocol, r.Ip)
	}
	timeStart := time.Now()

	resp = &httpx.Response{}
	var err error
	resp, err = r.do(fullUrl)
	if err != nil {
		if !retried {
			if protocol == httpx.HTTPS {
				protocol = httpx.HTTP
			} else {
				protocol = httpx.HTTPS
			}
			retried = true
			goto retry
		}
	}

	builder := &strings.Builder{}
	builder.WriteString(fullUrl)

	var title string
	if resp != nil {
		title = resp.Title
	}

	p, err := url.Parse(fullUrl)
	var ip string
	var ipArray []string
	if err != nil {
		ip = ""
	} else {
		hostname := p.Hostname()
		ip = r.Ht.Dialer.GetDialedIP(hostname)
		// ip为空，看看p.host是不是ip
		if ip == "" {
			address := net.ParseIP(hostname)
			if address != nil {
				ip = address.String()
			}
		}
	}
	dnsData, err := r.Ht.Dialer.GetDNSData(p.Host)
	if dnsData != nil && err == nil {
		ipArray = append(ipArray, dnsData.CNAME...)
		ipArray = append(ipArray, dnsData.A...)
		ipArray = append(ipArray, dnsData.AAAA...)
	}
	cname := strings.Join(ipArray, ",")

	// CDN检测
	cdn, err := r.Ht.CDNCheck(resp, r.Ip, cname)
	if err != nil {
		log2.Warn("[CDNCheck]:", err)
	}

	// 指纹处理
	fofaResults, err := r.Fofa.Matcher(resp, r.Output.Servers, r.Port)
	if err != nil {
		log2.Warn("[FOFAFinger]:", err)
	}
	var webbanner result.FingerResult
	if resp != nil {
		if resp.TLSData != nil {
			webbanner = result.FingerResult{
				Title:         title,
				TLSData:       resp.TLSData,
				ContentLength: resp.ContentLength,
				StatusCode:    resp.StatusCode,
				ResponseTime:  time.Since(timeStart).String(),
				Str:           builder.String(),
				Header:        resp.HeaderStr,
				FirstLine:     resp.FirstLine,
				Headers:       resp.Headers,
				DataStr:       resp.DataStr,
				Fingers:       fofaResults,
				CDN:           cdn,
			}
		} else {
			tlsdata := &httpx.TLSData{
				DNSNames:           nil,
				EmailAddresses:     nil,
				CommonName:         nil,
				Organization:       nil,
				IssuerCommonName:   nil,
				IssuerOrg:          nil,
				OrganizationalUnit: nil,
				Issuer:             nil,
				Subject:            nil,
			}
			webbanner = result.FingerResult{
				Title:         title,
				TLSData:       tlsdata,
				ContentLength: resp.ContentLength,
				StatusCode:    resp.StatusCode,
				ResponseTime:  time.Since(timeStart).String(),
				Str:           builder.String(),
				Header:        resp.HeaderStr,
				FirstLine:     resp.FirstLine,
				Headers:       resp.Headers,
				DataStr:       resp.DataStr,
				Fingers:       fofaResults,
				CDN:           cdn,
			}
		}
	} else {
		webbanner = result.FingerResult{}
	}
	return webbanner
}

func (r *Runner) Close() {
	r.Ht.Dialer.Close()
	_ = r.Hm.Close()
}

// Handlejob 任务处理
func Handlejob(c *redis2.Client, queue *job.Queue, taskstate *job.TaskState) {
	var targets []string
	// 查看消息队列，取出任务
	lenval := c.LLen(config.GlobalConfig.NodeConfig.NodeQueue)
	qlen := lenval.Val()
	if qlen > 0 { // 若队列不空
		for i := 1; i <= int(qlen); i++ {
			tmpjob := db.RecvJob(c)
			st := strings.Replace(tmpjob[1], "\"", "", -1)
			tmptargets := strings.Split(st, ",")
			taskstate.Tasks = len(tmptargets) - 1
			for k, v := range tmptargets {
				if k == 0 {
					taskstate.Name = v
				}
				if k != 0 && v != "" {
					targets = append(targets, v)
				}
			}
			log2.Info("[targets]:", targets)
		}
		for _, t := range targets {
			job.Push(queue, t) //将任务目标加入到任务队列中
		}
	}
}

// HandleTargets 生成扫描实例
func HandleTargets(queue *job.Queue, fofaPrints *fringerprint.FofaPrints) []*Runner {
	var targets []string
	var runners []*Runner
	for i := 0; i <= queue.Length; i++ {
		targets = append(targets, job.Pop(queue))
	}
	if len(targets) > 0 {
		for _, v := range targets {
			if !strings.Contains(v, "http") {
				target := strings.Split(v, ":")
				if len(target) > 0 {
					tmptarget := util.TargetsHandle(target[0]) //目标处理，若是c段地址，则返回一个ip段，若是单个ip，则直接返回单个ip切片，若是域名或url地址，则返回域名
					if len(target) == 3 && target[1] == "U" {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[2], t, "udp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[2], "", "udp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					} else {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[1], t, "tcp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[1], "", "tcp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					}

				}
			} else if strings.Contains(v, "https") {
				vtarget := v[8:]
				target := strings.Split(vtarget, ":")
				if len(v) > 0 {
					tmptarget := util.TargetsHandle(v) //目标处理，若是c段地址，则返回一个ip段，若是单个ip，则直接返回单个ip切片，若是域名或url地址，则返回域名
					if len(target) == 3 && target[1] == "U" {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[1], t, "udp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[1], "", "udp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					} else {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[1], t, "tcp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[1], "", "tcp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					}

				}
			} else if strings.Contains(v, "http") {
				vtarget := v[7:]
				target := strings.Split(vtarget, ":")
				if len(v) > 0 {
					tmptarget := util.TargetsHandle(v) //目标处理，若是c段地址，则返回一个ip段，若是单个ip，则直接返回单个ip切片，若是域名或url地址，则返回域名
					if len(target) == 3 && target[1] == "U" {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[1], t, "udp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[1], "", "udp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					} else {
						for _, t := range tmptarget {
							var runner2 *Runner
							var err1 error
							if strings.Contains(t, "com") || strings.Contains(t, "cn") {
								ip := getipbydomain.GetIPbyDomain(t)
								runner2, err1 = NewRunner(ip, target[1], t, "tcp", fofaPrints)
							} else {
								runner2, err1 = NewRunner(t, target[1], "", "tcp", fofaPrints)
							}
							if err1 != nil {
								log2.Error("[HandleTargets]:", err1)
							}
							runners = append(runners, runner2)
						}
					}
				}
			}

		}
	}
	if len(runners) > 0 {
		return runners
	}
	return nil
}

// // Scan 扫描函数
// func Scan(target *Runner, GoNmap *gonmap.VScan, region *ipinfo.Ip2Region) *result.Output {
// 	// 域名存在与否
// 	if target.Domain != "" {
// 		// 主机存活探测
// 		if icmp.IcmpCheckAlive(target.Domain, target.Ip) || ping.PingCheckAlive(target.Domain) || httpcheck.HttpCheck(target.Domain, target.Port, target.Ip) || tcp.TcpCheckAlive(target.Ip, target.Port) {

// 			if tcp.TcpCheckAlive(target.Ip, target.Port) || httpcheck.HttpCheck(target.Domain, target.Port, target.Ip) {
// 				// 普通端口探测
// 				nmapbanner, err := gonmap.GoNmapScan(GoNmap, target.Ip, target.Port, target.Protocol)
// 				if nmapbanner != nil {
// 					target.Output.Servers = nmapbanner
// 				}
// 				if strings.Contains(target.Output.Servers.Banner, "HTTP") {
// 					target.Output.Servers.Name = "http"
// 					target.Output.Servername = "http"
// 				} else {
// 					target.Output.Servername = nmapbanner.Name

// 				}
// 				// web端口探测
// 				webresult := result.FingerResult{}
// 				if target.Output.Servername == "http" {
// 					webresult = target.Request()
// 				}
// 				target.Output.Webbanner = webresult
// 				target.Output.Ip = target.Ip
// 				target.Output.Port = target.Port
// 				target.Output.Protocol = strings.ToUpper(target.Protocol)
// 				target.Output.Domain = target.Domain

// 				if webresult.Header != "" {
// 					target.Output.Banner = target.Output.Webbanner.Header
// 				} else {
// 					target.Output.Banner = nmapbanner.Banner
// 				}
// 				// ip信息查询
// 				info, err := ipinfo.GetIpinfo(region, target.Ip)
// 				if err != nil {
// 					log2.Warn("[GetIPInfo]:", err)
// 				}
// 				target.Output.City = info.City
// 				target.Output.Region = info.Region
// 				target.Output.ISP = info.ISP
// 				target.Output.CityId = info.CityId
// 				target.Output.Province = info.Province
// 				target.Output.Country = info.Country
// 				target.Output.TargetId = target.Ip + "-" + target.Port + "-" + target.Domain
// 				if target.Output.Port == "80" {
// 					target.Output.Target = "http://" + target.Domain
// 				} else {
// 					target.Output.Target = "http://" + target.Domain + ":" + target.Output.Port
// 				}
// 				target.Output.LastTime = time.Now().Format("2006-01-02 15:04:05")
// 				return target.Output
// 			}
// 		}
// 	} else {
// 		if cdncheck.IPCDNCheck(target.Ip) != true { //判断IP是否存在CDN

// 			// 主机存活探测
// 			if icmp.IcmpCheckAlive("", target.Ip) || ping.PingCheckAlive(target.Ip) || httpcheck.HttpCheck(target.Domain, target.Port, target.Ip) || tcp.TcpCheckAlive(target.Ip, target.Port) {

// 				// 普通端口探测
// 				nmapbanner, err := gonmap.GoNmapScan(GoNmap, target.Ip, target.Port, target.Protocol)
// 				if nmapbanner != nil {
// 					target.Output.Servers = nmapbanner
// 				}
// 				if strings.Contains(target.Output.Servers.Banner, "HTTP") {
// 					target.Output.Servers.Name = "http"
// 					target.Output.Servername = "http"
// 				} else {
// 					target.Output.Servername = nmapbanner.Name
// 				}
// 				// web端口探测
// 				webresult := result.FingerResult{}
// 				if target.Output.Servername == "http" {
// 					webresult = target.Request()
// 				}
// 				target.Output.Webbanner = webresult
// 				target.Output.Ip = target.Ip
// 				target.Output.Port = target.Port
// 				target.Output.Protocol = strings.ToUpper(target.Protocol)
// 				target.Output.Domain = ""

// 				if webresult.Header != "" {
// 					target.Output.Banner = target.Output.Webbanner.Header
// 				} else {
// 					target.Output.Banner = nmapbanner.Banner
// 				}
// 				// ip信息查询
// 				info, err := ipinfo.GetIpinfo(region, target.Ip)
// 				if err != nil {
// 					log2.Warn("[GetIPInfo]:", err)
// 				}
// 				target.Output.City = info.City
// 				target.Output.Region = info.Region
// 				target.Output.ISP = info.ISP
// 				target.Output.CityId = info.CityId
// 				target.Output.Province = info.Province
// 				target.Output.Country = info.Country
// 				target.Output.TargetId = target.Ip + "-" + target.Port + "-" + target.Domain
// 				if target.Output.Port == "80" {
// 					target.Output.Target = "http://www." + target.Ip
// 				} else {
// 					target.Output.Target = "http://www." + target.Ip + ":" + target.Port
// 				}
// 				target.Output.LastTime = time.Now().Format("2006-01-02 15:04:05")
// 				return target.Output
// 			}
// 		}
// 	}
// 	return nil
// }

// Scan function performs concurrent scans to quickly determine the availability of a target host
// and then performs a detailed scan on alive hosts.
func Scan(target *Runner, GoNmap *gonmap.VScan, region *ipinfo.Ip2Region) *result.Output {
	output := &result.Output{}

	// Using a wait group to synchronize goroutines
	var wg sync.WaitGroup
	results := make(chan bool, 4)

	// Perform ICMP, Ping, HTTP, and TCP checks in parallel
	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- icmp.IcmpCheckAlive(target.Domain, target.Ip)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- ping.PingCheckAlive(target.Ip)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- httpcheck.HttpCheck(target.Domain, target.Port, target.Ip)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- tcp.TcpCheckAlive(target.Ip, target.Port)
	}()

	// Wait for all checks to complete
	wg.Wait()
	close(results)

	// Evaluate results to determine if the host is alive
	hostAlive := false
	for result := range results {
		if result {
			hostAlive = true
			break
		}
	}

	if !hostAlive {
		return nil // Exit early if no checks indicate the host is alive
	}

	// Proceed with a detailed Nmap scan if the host is confirmed to be alive
	if nmapbanner, err := gonmap.GoNmapScan(GoNmap, target.Ip, target.Port, target.Protocol); err == nil {
		output.Servers = nmapbanner
		// nmapbanner, err := gonmap.GoNmapScan(GoNmap, target.Ip, target.Port, target.Protocol)
		// 				if nmapbanner != nil {
		// 					target.Output.Servers = nmapbanner
		// 				}

		// Additional details could be processed here such as HTTP fingerprinting
		if target.Output.Servername == "http" {
			webresult := target.Request()
			output.Webbanner = webresult
			if webresult.Header != "" {
				output.Banner = webresult.Header
			}
		}

		// Obtain IP information
		if info, err := ipinfo.GetIpinfo(region, target.Ip); err == nil {
			output.City = info.City
			output.Region = info.Region
			output.ISP = info.ISP
			output.CityId = info.CityId
			output.Province = info.Province
			output.Country = info.Country
		} else {
			log.Printf("Error obtaining IP info: %v", err)
		}

		// Prepare final output details
		output.Ip = target.Ip
		output.Port = target.Port
		output.Protocol = target.Protocol
		output.Domain = target.Domain
		output.LastTime = result.FormatTimeNow()
		return output
	} else {
		log.Printf("Error performing Nmap scan: %v", err)
	}

	return nil
}

// 为了提高 runner.go 中 Scan 功能的扫描效率，请考虑以下优化：

// 并行化扫描操作：利用 goroutines 并行执行 ICMP、Ping、HTTP 和 TCP 检查，而不是按顺序执行。这可以大大减少等待每次检查完成所花费的时间。

// 减少冗余扫描：实现逻辑以避免多次执行相同类型的扫描。例如，如果 ICMP 检查确定主机已关闭，请跳过后续的 Ping、HTTP 和 TCP 检查。

// 集成缓存：缓存以前扫描的结果，以避免在短时间内重复扫描相同的目标。这对于经常扫描的目标特别有用。

// 优化 Nmap 参数：通过减少重试次数、将扫描限制为更少的端口或使用更激进的时序选项，自定义 Nmap 扫描参数以专注于速度。

// 基于条件的选择性扫描：实现条件逻辑以仅执行详细的 Nmap 扫描
