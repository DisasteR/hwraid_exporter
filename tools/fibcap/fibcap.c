/*
 * fibcap : bibliothèque LD_PRELOAD qui journalise les ioctl de gestion
 * aacraid d'un processus : capture des commandes émises par l'outil de
 * gestion du constructeur et des réponses du firmware (exigence S4 du cahier
 * des charges, docs/guide-capture-aacraid.md).
 *
 * Le journal contient les réponses du firmware, donc des numéros de série et
 * des WWN : il ne doit pas être publié tel quel.
 *
 * Principe : la fonction ioctl de la glibc est remplacée par celle-ci, qui
 * transmet l'appel tel quel au noyau (appel système direct) et, pour les
 * codes de gestion aacraid, écrit dans un journal :
 *   - le code ioctl, le résultat et errno ;
 *   - pour FSACTL_SENDFIB / FSACTL_SEND_LARGE_FIB, les octets de la FIB
 *     avant l'appel (requête) et après (réponse), sur la plage exacte que
 *     le pilote copie : max(Size + 32, SenderSize), plafonnée à 2048.
 * Rien n'est modifié dans les tampons ni dans le résultat.
 *
 * Journal : $HWRAID_FIBCAP_OUT, obligatoire, chemin absolu d'un fichier qui
 * n'existe pas encore. Il est créé en mode 0600, sans suivre de lien
 * symbolique (O_EXCL | O_NOFOLLOW). Sans journal, rien n'est lu ni écrit :
 * les appels sont transmis tels quels. Une ligne par événement, écrite en un
 * seul appel write ; une écriture incomplète est signalée sur la sortie
 * d'erreur.
 *
 * Limites : x86-64, glibc. Les FIB ne sont lues qu'aux plages que le pilote
 * lit lui-même (ioctl_send_fib) : un appelant qui passe un tampon plus court
 * provoquerait l'erreur EFAULT sans fibcap, et une faute de segmentation
 * avec fibcap.
 *
 * Seuls des symboles anciens de la glibc sont utilisés (syscall, getenv),
 * pour fonctionner sur des distributions plus anciennes que celle de
 * compilation.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <unistd.h>

#define FSACTL_SENDFIB 0x42008UL
#define FSACTL_SEND_LARGE_FIB 0x42168UL
#define FIB_HDR 32
#define FIB_MAX 2048

static int out_fd = -1;
static unsigned long seq;

static void warn(const char *m)
{
	size_t n = 0;
	while (m[n])
		n++;
	syscall(SYS_write, 2, m, n);
}

__attribute__((constructor)) static void fibcap_init(void)
{
	int saved = errno;
	const char *p = getenv("HWRAID_FIBCAP_OUT");
	if (!p || p[0] != '/') {
		warn("fibcap : HWRAID_FIBCAP_OUT absent ou relatif, aucune capture\n");
		errno = saved;
		return;
	}
	/* O_WRONLY|O_CREAT|O_EXCL|O_APPEND|O_NOFOLLOW|O_CLOEXEC, mode 0600 */
	out_fd = (int)syscall(SYS_openat, -100, p, 01 | 0100 | 0200 | 02000 | 0400000 | 02000000, 0600);
	if (out_fd < 0)
		warn("fibcap : journal impossible à créer (existe déjà ?), aucune capture\n");
	errno = saved;
}

/* Code de gestion aacraid : CTL_CODE(fn, méthode) = 4<<16 | fn<<2 | m,
 * plus les trois codes historiques non encodés. */
static int is_aac(unsigned long req)
{
	return (req & ~0xffffUL) == 0x40000UL || req == 0x163 || req == 0x173 || req == 2131;
}

struct buf {
	char b[64 + 2 * 2 * FIB_MAX + 256];
	size_t n;
};

static void put(struct buf *o, const char *s)
{
	while (*s && o->n < sizeof(o->b) - 1)
		o->b[o->n++] = *s++;
}

static void putnum(struct buf *o, unsigned long v, int base)
{
	char t[32];
	int i = 0;
	do {
		t[i++] = "0123456789abcdef"[v % base];
		v /= base;
	} while (v && i < (int)sizeof(t));
	while (i > 0 && o->n < sizeof(o->b) - 1)
		o->b[o->n++] = t[--i];
}

static void puthex(struct buf *o, const unsigned char *p, size_t len)
{
	for (size_t i = 0; i < len && o->n < sizeof(o->b) - 3; i++) {
		o->b[o->n++] = "0123456789abcdef"[p[i] >> 4];
		o->b[o->n++] = "0123456789abcdef"[p[i] & 15];
	}
}

static size_t fib_len(const unsigned char *f)
{
	size_t size = (size_t)(f[8] | f[9] << 8) + FIB_HDR;
	size_t sender = (size_t)(f[10] | f[11] << 8);
	if (sender > size)
		size = sender;
	return size > FIB_MAX ? FIB_MAX : size;
}

static void record(unsigned long id, const char *phase, unsigned long req,
		   long ret, int err, const unsigned char *fib, size_t len)
{
	struct buf o;
	struct {
		long sec, nsec;
	} ts = {0, 0};
	o.n = 0;
	syscall(SYS_clock_gettime, 0 /* CLOCK_REALTIME */, &ts);
	put(&o, "seq=");
	putnum(&o, id, 10);
	put(&o, " t=");
	putnum(&o, (unsigned long)ts.sec, 10);
	put(&o, ".");
	putnum(&o, (unsigned long)ts.nsec, 10);
	put(&o, " tid=");
	putnum(&o, (unsigned long)syscall(SYS_gettid), 10);
	put(&o, " phase=");
	put(&o, phase);
	put(&o, " ioctl=0x");
	putnum(&o, req, 16);
	if (phase[0] == 'R' && phase[1] == 'S') {
		put(&o, " ret=");
		if (ret < 0) {
			put(&o, "-");
			putnum(&o, (unsigned long)-ret, 10);
		} else {
			putnum(&o, (unsigned long)ret, 10);
		}
		put(&o, " errno=");
		putnum(&o, (unsigned long)err, 10);
	}
	if (fib) {
		put(&o, " len=");
		putnum(&o, len, 10);
		put(&o, " fib=");
		puthex(&o, fib, len);
	}
	o.b[o.n++] = '\n';
	if (syscall(SYS_write, out_fd, o.b, o.n) != (long)o.n)
		warn("fibcap : écriture du journal incomplète\n");
}

int ioctl(int fd, unsigned long req, ...)
{
	long ret;
	int err, saved = errno;
	va_list ap;
	va_start(ap, req);
	void *arg = va_arg(ap, void *);
	va_end(ap);
	if (out_fd < 0 || !is_aac(req)) {
		return (int)syscall(SYS_ioctl, fd, req, arg);
	}
	unsigned long id = __atomic_fetch_add(&seq, 1, __ATOMIC_RELAXED);
	int is_fib = (req == FSACTL_SENDFIB || req == FSACTL_SEND_LARGE_FIB) && arg;
	size_t len = is_fib ? fib_len((const unsigned char *)arg) : 0;
	record(id, "REQ", req, 0, 0, is_fib ? (const unsigned char *)arg : NULL, len);
	ret = syscall(SYS_ioctl, fd, req, arg);
	err = ret < 0 ? errno : 0;
	/* Après un échec, le tampon n'est pas une réponse : seul le code est
	 * journalisé. */
	record(id, "RSP", req, ret < 0 ? -err : ret, err,
	       (is_fib && ret >= 0) ? (const unsigned char *)arg : NULL, len);
	errno = ret < 0 ? err : saved;
	return (int)ret;
}
