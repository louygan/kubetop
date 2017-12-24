package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	//"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

var (
	colorNode       = color.New(color.FgYellow).SprintFunc()
	colorPod        = color.New(color.FgCyan).SprintFunc()
	colorService    = color.New(color.FgBlue).SprintFunc()
	colorDeployment = color.New(color.FgMagenta).SprintFunc()
	colorFailed     = color.New(color.FgRed).SprintFunc()
	colorWarning    = color.New(color.FgYellow).SprintFunc()

	flagNamespace = flag.String("namespace", "", "filter resources by namespace")
)

type (
	Row  []string
	Rows []Row
)

func (r Rows) Len() int      { return len(r) }
func (r Rows) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r Rows) Less(i, j int) bool {
	return fmt.Sprintf("%s", r[i]) < fmt.Sprintf("%s", r[j])
}

func main() {
	log.SetFlags(log.Lshortfile)

	flag.Parse()

	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}

	configFilepath := os.Getenv("KUBECONFIG")
	if len(configFilepath) == 0 {
		configFilepath = filepath.Join(usr.HomeDir, ".kube", "config")
	}

	fmt.Printf("Using %s\n", configFilepath)
	config, err := clientcmd.BuildConfigFromFlags("", configFilepath)
	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var nodeNames []string
	for _, node := range nodes.Items {
    nodeNames = append(nodeNames, node.ObjectMeta.Name)
	}
	lcpNodes := lcp(nodeNames)

	var rows Rows
	var ch chan Rows
	for {
		rows = make(Rows, 0)
		ch = make(chan Rows)

		go func() {
			for r := range ch {
				rows = append(rows, r...)
			}
		}()

		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); getNodes(ch, clientset) }()
		go func() { defer wg.Done(); getServices(ch, clientset) }()
		go func() { defer wg.Done(); getDeployments(ch, clientset) }()
		go func() { defer wg.Done(); getPods(ch, clientset, lcpNodes) }()
		wg.Wait()
		close(ch)

		clear()
		sort.Sort(rows)
		render(Row{
			"Type",
			"Namespace",
			"Name",
			"Status",
			"Node",
			"IPs",
			"Age",
		}, rows)
		time.Sleep(500 * time.Millisecond)
	}
}

func getNodes(ch chan Rows, clientset *kubernetes.Clientset) {
	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var rows Rows
	for _, node := range nodes.Items {
		if *flagNamespace != "" && node.ObjectMeta.Namespace != *flagNamespace {
			continue
		}
		var statuses []string
		if len(node.Status.Phase) > 0 {
			statuses = append(statuses, string(node.Status.Phase))
		}
		for _, c := range node.Status.Conditions {
			if c.Status != "True" {
				continue
			}
			statuses = append(statuses, string(c.Type))
		}
		addressesMap := make(map[string]bool)
		var addresses []string
		for _, addr := range node.Status.Addresses {
			if addressesMap[addr.Address] == true {
				continue
			}
			addressesMap[addr.Address] = true
			addresses = append(addresses, addr.Address)
		}

		rows = append(rows, Row{
			colorNode("[node]"),
			colorNode(node.ObjectMeta.Namespace),
			colorNode(node.ObjectMeta.Name),
			colorNode(strings.Join(statuses, " ")),
			colorNode(""), // Node
			colorNode(strings.Join(addresses, " ")),
			colorNode(shortHumanDuration(time.Since(node.CreationTimestamp.Time))),
		})
	}
	ch <- rows
}

func getServices(ch chan Rows, clientset *kubernetes.Clientset) {
	services, err := clientset.CoreV1().Services("").List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var rows Rows
	for _, service := range services.Items {
		if service.ObjectMeta.Namespace == "kube-system" {
			continue
		}
		if *flagNamespace != "" && service.ObjectMeta.Namespace != *flagNamespace {
			continue
		}
		var statuses []string
		for _, c := range service.Status.LoadBalancer.Ingress {
			statuses = append(statuses, fmt.Sprintf("%s %s", c.IP, c.Hostname))
		}
		var ports []string
		for _, c := range service.Spec.Ports {
			ports = append(ports, c.Name)
		}
		var ips []string
		for _, ip := range service.Spec.ExternalIPs {
			ips = append(ips, ip)
		}
		if service.Spec.ClusterIP != "" {
			ips = append(ips, service.Spec.ClusterIP)
		}
		rows = append(rows, Row{
			colorService("[svc]"),
			colorService(service.ObjectMeta.Namespace),
			colorService(service.ObjectMeta.Name),
			colorService(strings.Join(statuses, ",")),
			colorService(""), // Node
			colorService(strings.Join(ips, " ") + " " + strings.Join(ports, " ")),
			colorService(shortHumanDuration(time.Since(service.CreationTimestamp.Time))),
		})
	}
	ch <- rows
}

func getDeployments(ch chan Rows, clientset *kubernetes.Clientset) {
	deps, err := clientset.Extensions().Deployments("").List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var rows Rows
	for _, dep := range deps.Items {
		if dep.ObjectMeta.Namespace == "kube-system" {
			continue
		}
		if *flagNamespace != "" && dep.ObjectMeta.Namespace != *flagNamespace {
			continue
		}
		var statuses []string
		for _, c := range dep.Status.Conditions {
			if c.Status != "True" {
				continue
			}
			statuses = append(statuses, string(c.Type))
		}
		var status string
		if dep.Status.AvailableReplicas < *dep.Spec.Replicas {
			status = colorFailed(fmt.Sprintf("%d/%d/%d %s",
				dep.Status.AvailableReplicas,
				dep.Status.Replicas,
				*dep.Spec.Replicas,
				strings.Join(statuses, " "),
			))
		} else {
			status = colorDeployment(fmt.Sprintf("%d/%d/%d %s",
				dep.Status.AvailableReplicas,
				dep.Status.Replicas,
				*dep.Spec.Replicas,
				strings.Join(statuses, " "),
			))
		}
		rows = append(rows, Row{
			colorDeployment("[deploy]"),
			colorDeployment(dep.ObjectMeta.Namespace),
			colorDeployment(fmt.Sprintf("%v", dep.ObjectMeta.Name)),
			status,
			colorDeployment(""), // Node
			colorDeployment(""), // IP
			colorDeployment(shortHumanDuration(time.Since(dep.CreationTimestamp.Time))),
		})
	}
	ch <- rows
}

func getPods(ch chan Rows, clientset *kubernetes.Clientset, lcpNodes string) {
	pods, err := clientset.CoreV1().Pods("").List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var rows Rows
	for _, pod := range pods.Items {
		if pod.ObjectMeta.Namespace == "kube-system" {
			continue
		}
		if *flagNamespace != "" && pod.ObjectMeta.Namespace != *flagNamespace {
			continue
		}
		status := string(pod.Status.Phase)
		var statuses []string
		statuses = append(statuses, status)
		for _, c := range pod.Status.Conditions {
			if c.Status != "True" {
				continue
			}
			statuses = append(statuses, string(c.Type))
		}
		if status == "Running" {
			status = colorPod(strings.Join(statuses, " "))
		} else {
			status = colorFailed(strings.Join(statuses, " "))
		}
		rows = append(rows, Row{
			colorPod("[pod]"),
			colorPod(pod.ObjectMeta.Namespace),
			colorPod(fmt.Sprintf("%v", truncate(pod.ObjectMeta.Name))),
			status,
			colorPod(strings.TrimPrefix(pod.Spec.NodeName, lcpNodes)), // Node
			colorPod(pod.Status.PodIP), //pod.Status.HostIP, pod.ObjectMeta.Labels),
			colorPod(shortHumanDuration(time.Since(pod.CreationTimestamp.Time))),
		})
	}
	ch <- rows
}

func render(header Row, rows Rows) {
	for i, row := range rows {
		if len(header) != len(row) {
			log.Fatalf("len(header)=%d != len(row)=%d for row %d", len(header), len(rows), i)
		}
	}
	table := tablewriter.NewWriter(os.Stdout)
	table.SetAutoWrapText(false)
	table.SetHeader(header)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetBorder(false)
	table.SetColumnSeparator("")
	table.SetCenterSeparator("")
	for _, row := range rows {
		table.Append([]string(row))
	}
	table.Render()
}

func truncate(s string) string {
	const max = 20
	const rightLen = 5
	if len(s) < max {
		return s
	}
	return s[0:max-3-rightLen] + "..." + s[len(s)-rightLen:]
}

// shortHumanDuration is copied from
// k8s.io/kubernetes/pkg/kubectl/resource_printer.go
func shortHumanDuration(d time.Duration) string {
	// Allow deviation no more than 2 seconds(excluded) to tolerate machine time
	// inconsistence, it can be considered as almost now.
	if seconds := int(d.Seconds()); seconds < -1 {
		return fmt.Sprintf("<invalid>")
	} else if seconds < 0 {
		return fmt.Sprintf("0s")
	} else if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	} else if minutes := int(d.Minutes()); minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	} else if hours := int(d.Hours()); hours < 24 {
		return fmt.Sprintf("%dh", hours)
	} else if hours < 24*364 {
		return fmt.Sprintf("%dd", hours/24)
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

//  LCP is copied from https://rosettacode.org/wiki/Longest_common_prefix#Go
func lcp(l []string) string {
	switch len(l) {
	case 0:
		return ""
	case 1:
		return l[0]
	}
	// LCP of min and max (lexigraphically)
	// is the LCP of the whole set.
	min, max := l[0], l[0]
	for _, s := range l[1:] {
		switch {
		case s < min:
			min = s
		case s > max:
			max = s
		}
	}
	for i := 0; i < len(min) && i < len(max); i++ {
		if min[i] != max[i] {
			return min[:i]
		}
	}
	return min
}

func clear() {
	cmd := exec.Command("clear")
	cmd.Stdout = os.Stdout
	cmd.Run()
}
