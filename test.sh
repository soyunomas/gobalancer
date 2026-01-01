#!/bin/bash

# ==========================================
# ⚙️ CONFIGURACIÓN
# ==========================================
TOML_FILE="config.toml"

# 1. Identificadores para DETECTAR el tráfico (IPs de los Gateways)
GW_FIBRA_IP="192.168.24.1"
GW_VPN_IP="192.168.192.61"

# 2. Identificadores para LEER EL TOML (Nombres exactos en config.toml)
NAME_FIBRA="WAN_eno1"
NAME_VPN="ZeroTier_Backup"

# Lista de Targets (Mix Global)
TARGETS=(
    "9.9.9.9" "208.67.222.222" "4.2.2.1" "1.0.0.1"
    "baidu.com" "yahoo.co.jp" "naver.com" "shopee.sg"
    "bbc.co.uk" "spiegel.de" "cern.ch" "yandex.ru" "orange.fr"
    "globo.com" "mercadolibre.com" "whitehouse.gov" "unam.mx" "nasa.gov"
    "zoom.us" "slack.com" "adobe.com" "salesforce.com" "oracle.com"
)

# Colores
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

# ==========================================
# 🧠 LÓGICA DE EXTRACCIÓN DE PESOS
# ==========================================
get_weight() {
    local iface_name=$1
    # Busca el nombre, lee las siguientes 10 líneas, busca "weight", toma el valor y limpia espacios
    val=$(grep -A 10 "name = \"$iface_name\"" "$TOML_FILE" | grep -m 1 "weight =" | awk -F'=' '{print $2}' | tr -d ' "[:space:]')
    echo "${val:-1}" # Si falla o está vacío, devuelve 1 por defecto
}

echo -e "📖 Leyendo configuración de ${YELLOW}$TOML_FILE${NC}..."

# Extraer Pesos
W_FIBRA=$(get_weight "$NAME_FIBRA")
W_VPN=$(get_weight "$NAME_VPN")
TOTAL_WEIGHT=$((W_FIBRA + W_VPN))

# Calcular Porcentajes Esperados (Matemáticas enteras de Bash)
EXP_PCT_FIBRA=$(( (W_FIBRA * 100) / TOTAL_WEIGHT ))
EXP_PCT_VPN=$(( 100 - EXP_PCT_FIBRA ))

echo -e "   ⚖️  Pesos detectados: Fibra=${YELLOW}${W_FIBRA}${NC} | VPN=${YELLOW}${W_VPN}${NC}"
echo -e "   🔮 Predicción Teórica: ~${EXP_PCT_FIBRA}% Fibra / ~${EXP_PCT_VPN}% VPN"
echo ""

# ==========================================
# 🚀 EJECUCIÓN DEL TEST
# ==========================================
echo -e "🚀 ${GREEN}Iniciando Test de Balanceo Multipath${NC}"
echo "------------------------------------------------------------------"
printf "%-25s | %-15s | %s\n" "Destino" "Gateway IP" "Interfaz Usada"
echo "------------------------------------------------------------------"

count_fibra=0
count_vpn=0
count_err=0

for target in "${TARGETS[@]}"; do
    gw_ip=$(tracepath -n -m 1 "$target" 2>/dev/null | grep -m 1 " 1: " | awk '{print $2}')

    if [[ "$gw_ip" == "$GW_FIBRA_IP" ]]; then
        interface="${GREEN}🟢 WAN_eno1${NC}"
        ((count_fibra++))
    elif [[ "$gw_ip" == "$GW_VPN_IP" ]]; then
        interface="${BLUE}🔵 ZeroTier${NC}"
        ((count_vpn++))
    elif [[ -z "$gw_ip" ]]; then
        gw_ip="Timeout"
        interface="${RED}❌ Sin Respuesta${NC}"
        ((count_err++))
    else
        interface="${RED}❓ Desconocido${NC}"
    fi

    printf "%-25s | %-15s | %b\n" "$target" "$gw_ip" "$interface"
done

total=$((count_fibra + count_vpn))

# ==========================================
# 📊 RESULTADOS
# ==========================================
echo "------------------------------------------------------------------"
echo -e "📊 ${GREEN}RESUMEN ESTADÍSTICO${NC}"
echo "   Total Peticiones: $((total + count_err))"
echo -e "   Fibra (eno1):     ${count_fibra}"
echo -e "   VPN (ZeroTier):   ${count_vpn}"

if [ $total -gt 0 ]; then
    real_fibra=$(( 100 * count_fibra / total ))
    real_vpn=$(( 100 - real_fibra ))
    
    echo ""
    echo -e "   ⚖️  Distribución Real:     ${real_fibra}% Fibra / ${real_vpn}% VPN"
    echo -e "   🎯  Distribución Esperada: ${EXP_PCT_FIBRA}% Fibra / ${EXP_PCT_VPN}% VPN"
    echo -e "       (Basado en pesos ${W_FIBRA}:${W_VPN})"
fi
echo "------------------------------------------------------------------"
