


PFRING_DIR=../PF_RING
CFLAGS=-O2

LIBS=${PFRING_DIR}/userland/lib/libpfring.a ${PFRING_DIR}/userland/libpcap/libpcap.a -L/usr/local/lib -lpthread -lrt -ldl -lm

cap: cap.c
	$(CC) $(CFLAGS) -o cap cap.c $(LIBS)
