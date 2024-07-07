<template>
  <video ref="videoPlayer" class="video-max" controls autoplay preload="auto">
    <source :src="source" :type="sourceType" />
    <track
      kind="subtitles"
      v-for="(sub, index) in subtitles"
      :key="index"
      :src="sub"
      :label="subLabel(sub)"
      :default="index === 0"
    />
    <p class="vjs-no-js">
      Sorry, your browser doesn't support embedded videos, but don't worry, you
      can <a :href="source">download it</a>
      and watch it with your favorite video player!
    </p>
  </video>
</template>

<script setup lang="ts">
import { ref, onMounted, nextTick } from "vue";

const videoPlayer = ref<HTMLVideoElement | null>(null);

const props = withDefaults(
  defineProps<{
    source: string;
    subtitles?: string[];
  }>(),
  {
    subtitles: () => [] as string[],
  }
);

const getSourceType = (source: string) => {
  const fileExtension = source ? source.split("?")[0].split(".").pop() : "";
  if (fileExtension?.toLowerCase() === "mkv") {
    return "video/mp4";
  }
  return `video/${fileExtension}`;
};

const source = ref(props.source);
const sourceType = ref(getSourceType(source.value));

nextTick(() => {
  if (videoPlayer.value) {
    videoPlayer.value.src = source.value;
    videoPlayer.value.load();
  }
});

onMounted(() => {
  if (videoPlayer.value) {
    videoPlayer.value.src = source.value;
    videoPlayer.value.load();
  }
});

const subLabel = (subUrl: string) => {
  let url: URL;
  try {
    url = new URL(subUrl);
  } catch (_) {
    // treat it as a relative url
    // we only need this for filename
    url = new URL(subUrl, window.location.origin);
  }

  const label = decodeURIComponent(
    url.pathname
      .split("/")
      .pop()!
      .replace(/\.[^/.]+$/, "")
  );

  return label;
};
</script>