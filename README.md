# 🚀 GoBalancer
**High-Performance Multi-WAN Load Balancer & Router Controller**

GoBalancer es un sistema de orquestación de red escrito en **Go (Golang)** diseñado para convertir cualquier máquina Linux en un router profesional con capacidades de **Multi-WAN (Múltiples proveedores de internet)**.

A diferencia de los scripts de shell tradicionales, GoBalancer actúa como un **Plano de Control (Control Plane)** inteligente: monitorea el estado de las conexiones en tiempo real y manipula el **Kernel de Linux** (Netlink/Iptables) dinámicamente para enrutar el tráfico de la manera más eficiente.

---

## 🔥 Características Principales

*   **⚡ Balanceo de carga ECMP:** Distribuye el tráfico entre múltiples enlaces (Fibra, 4G, Starlink) mediante *Weighted Round Robin* para aprovechar la capacidad total de la red.
*   **🛡️ Failover Automático:** Detecta caídas de internet y redirige el tráfico instantáneamente a líneas de respaldo.
*   **🧠 Monitor de Salud Inteligente:**
    *   Verificación dual: Ping ICMP + Handshake TCP (Puerto configurable).
    *   **Anti-Flapping (Histéresis):** Evita cambios constantes de ruta por micro-cortes.
*   **🔌 Modo Router (Gateway):** Activa nativamente NAT (Masquerade) y IP Forwarding en todas las interfaces WAN activas.
*   **🪄 Asistente de Configuración:** Detecta tu hardware automáticamente y genera la configuración por ti.
*   **🐧 Nativo de Linux:** Usa `netlink` (syscalls) para máximo rendimiento. Cero overhead en el tráfico de datos.

---

## 🛠️ Instalación y Compilación

### Requisitos Previos
*   Linux (Kernel 4.x o superior).
*   Permisos de `root` (necesario para modificar tablas de rutas).
*   Go 1.22+ (solo si vas a compilar).

### Compilación Rápida
El proyecto incluye un `Makefile` optimizado.

```bash
# 1. Clonar el repositorio
git clone https://github.com/soyunomas/gobalancer.git
cd gobalancer

# 2. Descargar dependencias
make deps

# 3. Compilar binario de producción (Estático y ligero)
make build-prod

# El ejecutable estará en: ./bin/gobalancer
```

---

## 🪄 Configuración Automática (Wizard)

No necesitas editar archivos a mano si no quieres. Hemos incluido un asistente inteligente que escanea tus tarjetas de red.

```bash
# Ejecutar el asistente (no requiere compilación previa)
go run cmd/wizard/main.go
```
El asistente detectará tus IPs, Gateways y generará el archivo `config.toml` automáticamente.

---

## ⚙️ Guía de Configuración Manual (`config.toml`)

El archivo `config.toml` es el corazón del sistema. A continuación se detallan todos los parámetros disponibles.

### Tabla de Parámetros

| Sección | Parámetro | Tipo | Valor por Defecto | Descripción |
| :--- | :--- | :--- | :--- | :--- |
| **[general]** | `check_interval` | String | `"2s"` | Frecuencia de chequeo de salud. |
| **[general]** | `algorithm` | String | `"weighted_round_robin"` | Estrategia (`weighted_round_robin` o `failover`). |
| **[lan]** | `iface_name` | String | `""` | *(Informativo)* Nombre de la interfaz LAN. |
| **[lan]** | `enable_dhcp` | Bool | `false` | Activa servidor DHCP interno (no implementado aún). |
| **[[interfaces]]**| `name` | String | *Requerido* | Nombre identificativo (ej: `"Fibra"`). |
| **[[interfaces]]**| `iface_name` | String | *Requerido* | Interfaz física de Linux (ej: `"eth0"`). |
| **[[interfaces]]**| `gateway` | String | *Requerido* | IP del Router del ISP. |
| **[[interfaces]]**| `interface_ip` | String | `""` (Automático) | IP local para bindear el monitor. Si se omite, se detecta sola. |
| **[[interfaces]]**| `weight` | Int | `1` | Peso para balanceo (1-100). |
| **[[interfaces]]**| `monitor_target` | String | `"8.8.8.8"` | IP pública para comprobar conectividad. |
| **[[interfaces]]**| `monitor_port` | Int | `53` | Puerto TCP para el check (53=DNS, 80=Web). |
| **[[interfaces]]**| `failures_to_down`| Int | `3` | Fallos consecutivos para marcar **DOWN**. |
| **[[interfaces]]**| `successes_to_up` | Int | `3` | Éxitos consecutivos para marcar **UP**. |
| **[[interfaces]]**| `max_latency` | String | `""` (Sin límite) | Latencia máxima permitida (SLA) antes de descartar la ruta (ej: `"150ms"`). |
| **[[rules]]** | `name` | String | *Requerido* | Nombre de la regla de enrutado. |
| **[[rules]]** | `type` | String | *Requerido* | Tipo de match (`port`, `dst_ip`, `src_ip`). |
| **[[rules]]** | `value` | String | *Requerido* | Valor a buscar (`80`, `1.1.1.1`). |
| **[[rules]]** | `target_interface`| String | *Requerido* | Nombre de la interfaz por donde saldrá el tráfico. |
| **[[rules]]** | `protocol` | String | `"tcp"` | Protocolo L4 (`tcp`, `udp`, `icmp`). |

---

## 💡 Escenarios de Uso

### Escenario A: El "Super PC" (Load Balancing Local)
*Objetivo:* Tienes un PC con tarjeta de cable (1Gbps) y una tarjeta Wifi (600Mbps). Quieres que tus descargas usen ambas a la vez. No compartes internet con nadie más.

**Configuración (`config.toml`):**
```toml
[general]
check_interval = "2s"
algorithm = "weighted_round_robin"

# NO definimos sección [lan], ya que es para uso propio.

[[interfaces]]
name = "Cable_Primario"
iface_name = "eth0"
gateway = "192.168.1.1"
weight = 3                  # Prefiere el cable
monitor_target = "8.8.8.8"

[[interfaces]]
name = "Wifi_Secundario"
iface_name = "wlan0"
gateway = "192.168.0.1"
weight = 2                  # Usa el wifi también
monitor_target = "1.1.1.1"
```

### Escenario B: Router Dedicado para Oficina (Gateway)
*Objetivo:* Un Mini-PC con 3 tarjetas de red actúa como router para toda la oficina.
*   `eth0`: Fibra Óptica (ISP 1).
*   `eth1`: Backup 4G (ISP 2).
*   `eth2`: Red Local (Switch de la oficina).

**Configuración (`config.toml`):**
```toml
[general]
check_interval = "1s"
algorithm = "failover"  # Modo ahorro: Solo usa 4G si la fibra cae

# GoBalancer habilita NAT en las WANs.
# Asegúrate de tener una política de FORWARD permisiva o reglas de firewall externas.
[lan]
iface_name = "eth2" # Referencia
enable_dhcp = false

[[interfaces]]
name = "ISP_Fibra"
iface_name = "eth0"
gateway = "200.10.10.1"
interface_ip = "200.10.10.5"
weight = 1
monitor_target = "8.8.8.8"
failures_to_down = 3

[[interfaces]]
name = "ISP_4G_Backup"
iface_name = "eth1"
gateway = "192.168.8.1"
interface_ip = "192.168.8.100"
weight = 1
monitor_target = "1.0.0.1"
successes_to_up = 10
```

### Escenario C: Híbrido (ISP Local + VPN Remota/ZeroTier)
*Objetivo:* Usar tu conexión de fibra local Y, simultáneamente, una conexión remota a través de un túnel VPN (ZeroTier, WireGuard, Tailscale) para sumar ancho de banda o tener una IP de salida en otro país.

```toml
[general]
check_interval = "2s"
algorithm = "weighted_round_robin"

# --- WAN 1: Tu Conexión Física ---
[[interfaces]]
name = "ISP_Principal"
iface_name = "eno1"
gateway = "192.168.24.1"        # Router de tu casa/oficina
weight = 3                      # Prioridad Alta (Fibra)
monitor_target = "8.8.8.8"
monitor_port = 53

# --- WAN 2: ZeroTier (Túnel VPN) ---
[[interfaces]]
name = "ZeroTier_Backup"
iface_name = "ztrfydfgcw"       # Nombre de la interfaz del túnel
gateway = "192.168.192.61"      # IP del Servidor Remoto (Exit Node)
interface_ip = "192.168.192.239"# Tu IP dentro del túnel
weight = 1                      # Prioridad Baja (Mayor latencia)
monitor_target = "1.1.1.1"      # Monitor Cloudflare
monitor_port = 53
```

### Escenario D: Single-NIC Multi-Gateway + ZeroTier
*Objetivo:* Caso complejo en Datacenter o Red Corporativa. Tienes **una sola interfaz física** (`eth0`) pero tu proveedor te ofrece **dos routers/gateways distintos** en la misma subred (ej: `10.0.0.1` y `10.0.0.2`) para redundancia. Además, tienes una VPN de respaldo.

*Nota: Debes haber asignado previamente las múltiples IPs (Alias) a tu tarjeta de red (ej: `ip addr add 10.0.0.101/24 dev eth0` y `10.0.0.102/24 dev eth0`).*

```toml
[general]
check_interval = "1s"
algorithm = "weighted_round_robin"

# --- Gateway A (ISP Primario Ruta 1) ---
[[interfaces]]
name = "ISP_Ruta_A"
iface_name = "eth0"         # Misma interfaz física
gateway = "10.0.0.1"        # Primer Router
interface_ip = "10.0.0.101" # IP Alias 1 (Importante: Distinta para diferenciar tráfico)
weight = 5
monitor_target = "8.8.8.8"

# --- Gateway B (ISP Primario Ruta 2) ---
[[interfaces]]
name = "ISP_Ruta_B"
iface_name = "eth0"         # Misma interfaz física
gateway = "10.0.0.2"        # Segundo Router
interface_ip = "10.0.0.102" # IP Alias 2
weight = 5
monitor_target = "1.1.1.1"  # Monitor distinto recomendado

# --- Respaldo VPN ---
[[interfaces]]
name = "Backup_ZeroTier"
iface_name = "zt7nn23"
gateway = "192.168.192.1"
interface_ip = "192.168.192.50"
weight = 1
monitor_target = "9.9.9.9"
```

---

## ⚓ Advanced: VPN Tunneling & Exit Nodes

Esta sección es crucial si usas ZeroTier o WireGuard como una de tus WANs (Escenario C).

### Configuración del Servidor Remoto (Exit Node)
Para que tu VPN funcione como salida a internet, debes configurar el **servidor remoto** (el que tiene la IP `192.168.192.61` en el ejemplo) para que haga NAT.

Ejecuta esto en el **Servidor Remoto** (Ubuntu/Debian):

```bash
# 1. Activar IP Forwarding
echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward

# 2. Definir interfaz de salida a internet (ej: eth0, enp7s0)
WAN_IFACE="eth0"

# 3. Activar NAT (Masquerade)
sudo iptables -t nat -I POSTROUTING 1 -o $WAN_IFACE -j MASQUERADE

# 4. Permitir tráfico de ZeroTier hacia Internet
sudo iptables -I FORWARD 1 -i zt+ -o $WAN_IFACE -j ACCEPT

# 5. Permitir tráfico de retorno (Established)
sudo iptables -I FORWARD 1 -i $WAN_IFACE -o zt+ -m state --state RELATED,ESTABLISHED -j ACCEPT
```

*Nota: GoBalancer configura automáticamente las rutas locales en el cliente para que el monitoreo funcione correctamente a través del túnel.*

---

## 🤖 Despliegue en Producción (Systemd)

Para que el balanceador arranque automáticamente al encender el equipo:

1.  Edita el archivo de servicio: `gobalancer.service` (asegura que las rutas sean correctas).
2.  Instala el servicio:

```bash
sudo cp gobalancer.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable gobalancer
sudo systemctl start gobalancer
```

3.  Verificar logs en tiempo real:
```bash
journalctl -u gobalancer -f
```

---

## ❓ Preguntas Frecuentes (FAQ)

**¿GoBalancer asigna IPs a mis dispositivos (DHCP)?**
No. GoBalancer se encarga del **enrutamiento crítico y failover**. Para asignar IPs en tu LAN, te recomendamos instalar `dnsmasq` o `isc-dhcp-server` en la misma máquina. Es el estándar de la industria (Unix Philosophy: "Do one thing and do it well").

**¿Puedo sumar las velocidades en una sola descarga?**
Depende.
*   **BitTorrent / Steam / Gestores de descarga:** **SÍ**. Estos programas abren múltiples conexiones simultáneas, que GoBalancer repartirá entre tus WANs.
*   **Descarga de archivo simple en navegador:** **NO**. Una sola conexión TCP (socket) debe ir por una sola ruta física. Sin embargo, el navegador podrá abrir otras conexiones (imágenes, scripts) por la otra línea, agilizando la carga web.

**¿Qué pasa con las webs de bancos (HTTPS)?**
El sistema usa `iptables` con reglas de NAT estándar. Sin embargo, para entornos críticos, se recomienda configurar "Sticky Sessions" (persistencia) para que un usuario no cambie de IP pública en mitad de una sesión bancaria.

---

**Licencia:** MIT  
**Autor:** Soyunomas
