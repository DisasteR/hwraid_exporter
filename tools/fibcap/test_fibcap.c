/* Programme de test de fibcap : émet des ioctl sur /dev/null (le noyau
 * répond ENOTTY) et vérifie que le résultat et errno sont transmis intacts.
 * Le contenu du journal est vérifié par test.sh. */
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

static unsigned char fib[2048];

static void *worker(void *p)
{
	int fd = open("/dev/null", O_RDONLY);
	unsigned char f[512];
	memset(f, 0x11, sizeof(f));
	f[8] = 0x20, f[9] = 0;    /* Size = 32 -> 64 octets */
	f[10] = 0, f[11] = 0;     /* SenderSize = 0 */
	for (int i = 0; i < 50; i++)
		ioctl(fd, 0x42008UL, f);
	close(fd);
	return p;
}

int main(void)
{
	int fd = open("/dev/null", O_RDONLY);
	int fails = 0;

	/* FIB : Size = 0x01e0 (480) + 32 = 512 octets ; SenderSize = 0x0200. */
	for (int i = 0; i < 2048; i++)
		fib[i] = (unsigned char)i;
	fib[8] = 0xe0, fib[9] = 0x01;
	fib[10] = 0x00, fib[11] = 0x02;
	errno = 0;
	int r = ioctl(fd, 0x42008UL, fib);
	if (r != -1 || errno != ENOTTY) {
		printf("SENDFIB: r=%d errno=%d (attendu -1/ENOTTY)\n", r, errno);
		fails++;
	}
	/* SenderSize plus grand que Size+32 : plage = SenderSize (0x0300 = 768). */
	fib[8] = 0x10, fib[9] = 0x00;
	fib[10] = 0x00, fib[11] = 0x03;
	ioctl(fd, 0x42168UL, fib);
	/* Taille aberrante : plafonnée à 2048. */
	fib[8] = 0xff, fib[9] = 0xff;
	ioctl(fd, 0x42008UL, fib);
	/* Pointeur invalide : pas de faute de segmentation, l'appel est
	 * transmis tel quel (le noyau répond ENOTTY sur /dev/null). */
	errno = 0;
	r = ioctl(fd, 0x42008UL, (void *)8);
	if (r != -1 || errno != ENOTTY) {
		printf("pointeur invalide : r=%d errno=%d\n", r, errno);
		fails++;
	}
	/* errno préservé sur le chemin non journalisé. */
	errno = 1234;
	ioctl(fd, 0x42198UL, fib);
	if (errno != ENOTTY) {
		printf("errno=%d après échec (attendu ENOTTY)\n", errno);
		fails++;
	}
	/* ioctl de gestion sans FIB (GET_HBA_INFO) : code seul. */
	ioctl(fd, 0x42198UL, fib);
	/* ioctl étranger : non journalisé, mais transmis (errno ENOTTY). */
	errno = 0;
	r = ioctl(fd, TCGETS, fib);
	if (r != -1 || errno != ENOTTY) {
		printf("TCGETS: r=%d errno=%d\n", r, errno);
		fails++;
	}
	/* Concurrence : 4 threads x 50 appels. */
	pthread_t th[4];
	for (int i = 0; i < 4; i++)
		pthread_create(&th[i], NULL, worker, NULL);
	for (int i = 0; i < 4; i++)
		pthread_join(th[i], NULL);
	close(fd);
	printf(fails ? "ECHEC\n" : "OK\n");
	return fails;
}
