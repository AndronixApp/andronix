/*
 * libandronixpresent.so: the app's end of the VirGL server's zero-copy
 * present (andronix_present.c in android.patch). JNI for
 * studio.com.techriz.andronix.standalone.display.GpuPresent: listen for the
 * server, receive its AHardwareBuffers and per-frame messages with fences,
 * and on the app's GL thread import a buffer as a texture and wait for a
 * frame's fence on the GPU.
 * MIT, Andronix.
 */
#include <android/hardware_buffer.h>
#include <android/hardware_buffer_jni.h>
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <jni.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#define FN(name) Java_studio_com_techriz_andronix_standalone_display_GpuPresent_##name

JNIEXPORT jint JNICALL FN(nativeListen)(JNIEnv *env, jclass cls, jstring jpath)
{
	(void)cls;
	const char *path = (*env)->GetStringUTFChars(env, jpath, NULL);
	struct sockaddr_un a = { .sun_family = AF_UNIX };
	int fd = -1;
	if (strlen(path) < sizeof(a.sun_path)) {
		strcpy(a.sun_path, path);
		unlink(path);
		fd = socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
		if (fd >= 0 && (bind(fd, (struct sockaddr *)&a, sizeof(a)) != 0 || listen(fd, 2) != 0)) {
			close(fd);
			fd = -1;
		}
		if (fd >= 0)
			chmod(path, 0600);
	}
	(*env)->ReleaseStringUTFChars(env, jpath, path);
	return fd;
}

JNIEXPORT jint JNICALL FN(nativeAccept)(JNIEnv *env, jclass cls, jint lfd)
{
	(void)env; (void)cls;
	return accept4(lfd, NULL, NULL, SOCK_CLOEXEC);
}

JNIEXPORT void JNICALL FN(nativeClose)(JNIEnv *env, jclass cls, jint fd)
{
	(void)env; (void)cls;
	if (fd >= 0) {
		shutdown(fd, SHUT_RDWR);
		close(fd);
	}
}

/* [width, height, count], or null. */
JNIEXPORT jintArray JNICALL FN(nativeHello)(JNIEnv *env, jclass cls, jint fd)
{
	(void)cls;
	uint32_t m[5];
	if (recv(fd, m, sizeof(m), 0) != (ssize_t)sizeof(m) || m[0] != 0x50584e41 || m[1] != 1)
		return NULL;
	jint out[3] = { (jint)m[2], (jint)m[3], (jint)m[4] };
	jintArray a = (*env)->NewIntArray(env, 3);
	(*env)->SetIntArrayRegion(env, a, 0, 3, out);
	return a;
}

JNIEXPORT jobject JNICALL FN(nativeRecvBuffer)(JNIEnv *env, jclass cls, jint fd)
{
	(void)cls;
	AHardwareBuffer *b = NULL;
	if (AHardwareBuffer_recvHandleFromUnixSocket(fd, &b) != 0 || !b)
		return NULL;
	jobject hb = AHardwareBuffer_toHardwareBuffer(env, b);
	AHardwareBuffer_release(b); /* the Java object holds its own reference */
	return hb;
}

/* [index, flags, seq, fence fd or -1], or null when the server went away. */
JNIEXPORT jintArray JNICALL FN(nativeRecvFrame)(JNIEnv *env, jclass cls, jint fd)
{
	(void)cls;
	uint32_t m[4];
	struct iovec iov = { m, sizeof(m) };
	char cbuf[CMSG_SPACE(sizeof(int))];
	struct msghdr h = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = cbuf, .msg_controllen = sizeof(cbuf) };
	if (recvmsg(fd, &h, MSG_CMSG_CLOEXEC) != (ssize_t)sizeof(m) || m[0] != 0x46584e41)
		return NULL;
	int fence = -1;
	struct cmsghdr *c = CMSG_FIRSTHDR(&h);
	if (c && c->cmsg_level == SOL_SOCKET && c->cmsg_type == SCM_RIGHTS)
		memcpy(&fence, CMSG_DATA(c), sizeof(int));
	jint out[4] = { (jint)m[1], (jint)m[2], (jint)m[3], fence };
	jintArray a = (*env)->NewIntArray(env, 4);
	(*env)->SetIntArrayRegion(env, a, 0, 4, out);
	return a;
}

/* On the GL thread: hb as texture tex. Returns the EGLImage (0 = failed). */
JNIEXPORT jlong JNICALL FN(nativeImport)(JNIEnv *env, jclass cls, jobject hb, jint tex)
{
	(void)cls;
	static PFNEGLGETNATIVECLIENTBUFFERANDROIDPROC get_cb;
	static PFNEGLCREATEIMAGEKHRPROC create_image;
	static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC target_tex;
	if (!get_cb) {
		get_cb = (PFNEGLGETNATIVECLIENTBUFFERANDROIDPROC)eglGetProcAddress("eglGetNativeClientBufferANDROID");
		create_image = (PFNEGLCREATEIMAGEKHRPROC)eglGetProcAddress("eglCreateImageKHR");
		target_tex = (PFNGLEGLIMAGETARGETTEXTURE2DOESPROC)eglGetProcAddress("glEGLImageTargetTexture2DOES");
	}
	if (!get_cb || !create_image || !target_tex)
		return 0;
	AHardwareBuffer *b = AHardwareBuffer_fromHardwareBuffer(env, hb);
	if (!b)
		return 0;
	static const EGLint attrs[] = { EGL_IMAGE_PRESERVED_KHR, EGL_TRUE, EGL_NONE };
	EGLImageKHR img = create_image(eglGetCurrentDisplay(), EGL_NO_CONTEXT, EGL_NATIVE_BUFFER_ANDROID, get_cb(b), attrs);
	if (img == EGL_NO_IMAGE_KHR)
		return 0;
	glBindTexture(GL_TEXTURE_2D, (GLuint)tex);
	target_tex(GL_TEXTURE_2D, (GLeglImageOES)img);
	return (jlong)(intptr_t)img;
}

JNIEXPORT void JNICALL FN(nativeDestroyImage)(JNIEnv *env, jclass cls, jlong img)
{
	(void)env; (void)cls;
	static PFNEGLDESTROYIMAGEKHRPROC destroy;
	if (!destroy)
		destroy = (PFNEGLDESTROYIMAGEKHRPROC)eglGetProcAddress("eglDestroyImageKHR");
	if (destroy && img)
		destroy(eglGetCurrentDisplay(), (EGLImageKHR)(intptr_t)img);
}

/* On the GL thread: the GPU waits for the server's blit (takes fd; -1 = nothing to wait for). */
JNIEXPORT jboolean JNICALL FN(nativeWaitFence)(JNIEnv *env, jclass cls, jint fd)
{
	(void)env; (void)cls;
	if (fd < 0)
		return JNI_TRUE;
	static PFNEGLCREATESYNCKHRPROC create_sync;
	static PFNEGLWAITSYNCKHRPROC wait_sync;
	static PFNEGLDESTROYSYNCKHRPROC destroy_sync;
	if (!create_sync) {
		create_sync = (PFNEGLCREATESYNCKHRPROC)eglGetProcAddress("eglCreateSyncKHR");
		wait_sync = (PFNEGLWAITSYNCKHRPROC)eglGetProcAddress("eglWaitSyncKHR");
		destroy_sync = (PFNEGLDESTROYSYNCKHRPROC)eglGetProcAddress("eglDestroySyncKHR");
	}
	if (!create_sync || !wait_sync || !destroy_sync) {
		close(fd);
		return JNI_FALSE;
	}
	EGLDisplay dpy = eglGetCurrentDisplay();
	const EGLint attrs[] = { EGL_SYNC_NATIVE_FENCE_FD_ANDROID, fd, EGL_NONE };
	EGLSyncKHR s = create_sync(dpy, EGL_SYNC_NATIVE_FENCE_ANDROID, attrs); /* owns fd from here */
	if (s == EGL_NO_SYNC_KHR) {
		close(fd);
		return JNI_FALSE;
	}
	wait_sync(dpy, s, 0);
	destroy_sync(dpy, s);
	return JNI_TRUE;
}
