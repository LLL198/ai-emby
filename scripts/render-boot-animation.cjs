/* Render the original AI Emby opening film. Requires @napi-rs/canvas and ffmpeg.
 * node scripts/render-boot-animation.cjs [canvas package path] [ffmpeg path]
 */
const renderArgs=process.argv.slice(2).filter(arg=>!arg.startsWith('--'));
const isNailong=process.argv.includes('--nailong');
const isLogin=process.argv.includes('--login-background');
const is4K=isLogin||isNailong||process.argv.includes('--4k');
const variant=isNailong ? {
  open:'ai-emby-boot-nailong-half-open-v3.png',closed:'ai-emby-boot-nailong-half-closed-v3.png',
  film:'ai-emby-boot-nailong-half-v5-4k.mp4',poster:'ai-emby-boot-nailong-half-poster-v5-4k.jpg',
  label:'NAILOONG / PLAY',subtitle:'电影要开始啦！'
} : {
  open:'ai-emby-boot-gold-open-v3.png',closed:'ai-emby-boot-gold-closed-v3.png',
  film:'ai-emby-boot-v4.mp4',poster:'ai-emby-boot-poster-v4.jpg',
  label:'GOLD / AZURE',subtitle:'欢迎进入你的私人影院'
};
if(isLogin){variant.film='ai-emby-login-background-v4-4k.mp4';variant.poster='ai-emby-login-poster-v4-4k.jpg';}
else if(is4K&&!isNailong){variant.film='ai-emby-boot-v5-4k.mp4';variant.poster='ai-emby-boot-poster-v5-4k.jpg';}
const { createCanvas, loadImage, GlobalFonts } = require(renderArgs[0] || '@napi-rs/canvas');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const fs = require('node:fs');
const path = require('node:path');
if (process.platform === 'win32') {
  const fonts = path.join(process.env.WINDIR || 'C:/Windows', 'Fonts');
  for (const [file, family] of [['msyh.ttc','Microsoft YaHei'], ['msyhbd.ttc','Microsoft YaHei'], ['consola.ttf','Consolas'], ['segoeui.ttf','Segoe UI'], ['segoeuiz.ttf','Segoe UI']]) {
    const fontPath = path.join(fonts,file);
    if(fs.existsSync(fontPath))GlobalFonts.registerFromPath(fontPath,family);
  }
}
const root = path.resolve(__dirname, '..');
const output = path.join(root, 'frontend/web/assets');
// Stop the login film on the last fully lit frame, before the opening's fade.
const W = 1920, H = 1080, FPS = 30, DURATION = isLogin?200/30:7.2;
const OUTPUT_SCALE=is4K?2:1;
const canvas = createCanvas(W*OUTPUT_SCALE, H*OUTPUT_SCALE), ctx = canvas.getContext('2d');
if(OUTPUT_SCALE>1)ctx.scale(OUTPUT_SCALE,OUTPUT_SCALE);
const clamp = n => Math.max(0, Math.min(1, n));
const smooth = (a,b,t) => { const x=clamp((t-a)/(b-a));return x*x*(3-2*x); };
const line = (points, color, width=1) => {
  ctx.beginPath();points.forEach((p,i)=>i?ctx.lineTo(...p):ctx.moveTo(...p));
  ctx.strokeStyle=color;ctx.lineWidth=width;ctx.stroke();
};
function text(str, x, y, size, color, font='Consolas, monospace') {
  ctx.font=`${size}px ${font}`;ctx.fillStyle=color;ctx.fillText(str,x,y);
}
function spaced(str,x,y,size,spacing,color) {
  ctx.font=`${size}px "Microsoft YaHei", "Segoe UI", sans-serif`;ctx.fillStyle=color;
  for(const ch of str){ctx.fillText(ch,x,y);x+=ctx.measureText(ch).width+spacing;}
}
function makeEdges(image) {
  const c=createCanvas(960,540),g=c.getContext('2d');
  g.filter='blur(0.8px)';g.drawImage(image,0,0,960,540);g.filter='none';
  const pixels=g.getImageData(0,0,960,540), src=pixels.data;
  const gray=new Float32Array(960*540);
  for(let i=0;i<gray.length;i++)gray[i]=src[i*4]*.2126+src[i*4+1]*.7152+src[i*4+2]*.0722;
  const result=g.createImageData(960,540), d=result.data;
  for(let y=1;y<539;y++)for(let x=1;x<959;x++) {
    const i=y*960+x;
    const gx=-gray[i-961]+gray[i-959]-2*gray[i-1]+2*gray[i+1]-gray[i+959]+gray[i+961];
    const gy=-gray[i-961]-2*gray[i-960]-gray[i-959]+gray[i+959]+2*gray[i+960]+gray[i+961];
    const m=Math.hypot(gx,gy);
    d[i*4]=135;d[i*4+1]=212;d[i*4+2]=255;
    d[i*4+3]=clamp((m-32)/95)*230*smooth(.31,.56,x/960);
  }
  g.putImageData(result,0,0);return c;
}
// Both keyframes share the same camera and character. Only the eye socket is
// animated, keeping hair, cheek, brow and background completely stable.
function makeEyeMotion(openImage,closedImage,geometry={
  left:1255,span:183,region:[1205,276,265,153],base:[345,22],closed:[350,17,18],
  upperArc:42,lowerArc:30,iris:[1349,350,47]
}) {
  const open=createCanvas(W,H),closed=createCanvas(W,H),pose=createCanvas(W,H);
  const og=open.getContext('2d'),cg=closed.getContext('2d'),pg=pose.getContext('2d');
  og.drawImage(openImage,0,0,W,H);cg.drawImage(closedImage,0,0,W,H);
  const sx=W/openImage.width,sy=H/openImage.height;
  // Landmarks are measured on each source plate, before output scaling.
  const {left,span}=geometry;
  const [rx,ry,rw,rh]=geometry.region;
  const region={x:Math.floor(rx*sx),y:Math.floor(ry*sy),w:Math.ceil(rw*sx),h:Math.ceil(rh*sy)};
  const a=og.getImageData(region.x,region.y,region.w,region.h),b=cg.getImageData(region.x,region.y,region.w,region.h);
  const result=pg.createImageData(region.w,region.h);
  function contours(x) {
    const u=clamp((x/sx-left)/span);
    const arc=geometry.round?Math.sqrt(Math.max(0,1-(2*u-1)**2)):Math.sin(Math.PI*u);
    const baseline=(geometry.base[0]+geometry.base[1]*u)*sy;
    return {u,arc,c:(geometry.closed[0]+geometry.closed[1]*u+geometry.closed[2]*arc)*sy,upper:baseline-geometry.upperArc*arc*sy,lower:baseline+geometry.lowerArc*arc*sy};
  }
  function sample(data,x,y,channel) {
    x=Math.max(0,Math.min(region.w-1,x));y=Math.max(0,Math.min(region.h-1,y));
    const x0=Math.floor(x),y0=Math.floor(y),x1=Math.min(x0+1,region.w-1),y1=Math.min(y0+1,region.h-1),fx=x-x0,fy=y-y0;
    return (data[(y0*region.w+x0)*4+channel]*(1-fx)+data[(y0*region.w+x1)*4+channel]*fx)*(1-fy)
      +(data[(y1*region.w+x0)*4+channel]*(1-fx)+data[(y1*region.w+x1)*4+channel]*fx)*fy;
  }
  function frame(p) {
    pg.clearRect(0,0,W,H);pg.drawImage(open,0,0);
    if(p>=1)return pose;
    for(let x=0;x<region.w;x++) {
      const eye=contours(x+region.x),upper=eye.c+(eye.upper-eye.c)*p,lower=eye.c+(eye.lower-eye.c)*p;
      // The iris bounds are narrower than the lash tips. Taper lid motion into
      // stationary corners instead of leaving a closed-skin rectangle there.
      const motionWeight=geometry.round?1:smooth(0,.16,eye.u)*smooth(0,.16,1-eye.u);
      for(let y=0;y<region.h;y++) {
        const gy=y+region.y,i=(y*region.w+x)*4;
        const feather=Math.min(smooth(0,22,x),smooth(0,22,region.w-1-x),smooth(0,20,y),smooth(0,20,region.h-1-y));
        let fromOpen=0,sourceY=gy,openY=gy;
        if(p>0&&eye.u>0&&eye.u<1) {
          if(gy<upper) {
            // The upper lid's skin and lash line physically travel upwards.
            sourceY=region.y+(gy-region.y)*(eye.c-region.y)/(upper-region.y);
            fromOpen=geometry.round?0:p*p;
            const lash=1-smooth(4*sy,22*sy,Math.abs(gy-upper));
            if(lash>0&&!geometry.round) { openY=eye.upper+(gy-upper);fromOpen=Math.max(fromOpen,lash*smooth(0,.16,p)); }
          } else if(gy>lower) {
            const lashMargin=12*sy*smooth(0,.2,p)*eye.arc;
            const start=eye.c+lashMargin,bottom=region.y+region.h;
            sourceY=start+(gy-lower)*(bottom-start)/(bottom-lower);
            fromOpen=geometry.round?0:p*p;
            const lash=1-smooth(4*sy,18*sy,Math.abs(gy-lower));
            if(lash>0&&!geometry.round) { openY=eye.lower+(gy-lower);fromOpen=Math.max(fromOpen,lash*smooth(0,.25,p)); }
          } else {
            // Reveal the unsqueezed iris behind the moving lids.
            fromOpen=1;
          }
        }
        for(let channel=0;channel<3;channel++) {
          const lid=sample(b.data,x,sourceY-region.y,channel);
          const opened=sample(a.data,x,openY-region.y,channel);
          const stationary=b.data[i+channel]*(1-p)+a.data[i+channel]*p;
          const moving=lid*(1-fromOpen)+opened*fromOpen;
          const morphed=stationary*(1-motionWeight)+moving*motionWeight;
          result.data[i+channel]=a.data[i+channel]*(1-feather)+morphed*feather;
        }
        result.data[i+3]=255;
      }
    }
    pg.putImageData(result,region.x,region.y);return pose;
  }
  function irisLight(p,t) {
    if(p<.15)return;
    ctx.save();ctx.beginPath();
    for(let i=0;i<=60;i++){
      const x=(left+span*i/60)*sx,e=contours(x),y=e.c+(e.upper-e.c)*p;
      if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);
    }
    for(let i=60;i>=0;i--){const x=(left+span*i/60)*sx,e=contours(x);ctx.lineTo(x,e.c+(e.lower-e.c)*p);}
    ctx.closePath();ctx.clip();
    const cx=geometry.iris[0]*sx,cy=geometry.iris[1]*sy,radius=geometry.iris[2]*sx;
    ctx.beginPath();ctx.ellipse(cx,cy,radius,radius*.91,0,0,Math.PI*2);ctx.clip();
    ctx.globalCompositeOperation='screen';
    const energy=smooth(.12,.9,p)*(.68+.32*Math.sin(t*2.1));
    const glow=ctx.createRadialGradient(cx,cy,9,cx,cy,radius);
    glow.addColorStop(0,`rgba(53,213,255,${.08*energy})`);glow.addColorStop(.68,`rgba(45,193,255,${.23*energy})`);glow.addColorStop(1,'rgba(36,161,238,0)');
    ctx.fillStyle=glow;ctx.fillRect(cx-radius,cy-radius,radius*2,radius*2);
    ctx.globalAlpha=.15*energy;ctx.fillStyle='#65ebff';
    for(let col=0;col<5;col++)for(let row=0;row<12;row++){
      if((col*3+row*7)%5===0)continue;
      ctx.fillRect(cx-23+col*10,cy-40+row*7,2,(col+row)%3===0?5:2);
    }
    ctx.restore();
  }
  return {frame,irisLight,region};
}
function makeNailongEyes(open,closed) {
  const eye=makeEyeMotion(open,closed,{
    left:1295,span:177,region:[1265,269,240,260],base:[402,-16],closed:[420,7,25],
    upperArc:96,lowerArc:96,iris:[1386,415,48],round:true
  });
  // Keep the meme's green eye matte; only the single eyelid moves.
  return {frame:eye.frame,irisLight() {}};
}
function draw(eyes,edges,t) {
  ctx.globalAlpha=1;ctx.globalCompositeOperation='source-over';ctx.fillStyle='#020508';ctx.fillRect(0,0,W,H);
  const scene=smooth(.12,.55,t), color=smooth(1.45,2.25,t);
  const opening=smooth(2.65,4.4,t),image=eyes.frame(opening,t);
  const zoom=isNailong?1:1.028-.014*smooth(.35,6.65,t), dx=isNailong?64:(W-W*zoom)*.807;
  const dy=(H-H*zoom)*.372;
  if(isNailong&&dx>0){ctx.save();ctx.globalAlpha=color*scene;ctx.drawImage(image,0,0,dx,H,0,0,dx,H);ctx.restore();}
  ctx.save();ctx.globalAlpha=color*scene;ctx.drawImage(image,dx,dy,W*zoom,H*zoom);ctx.restore();
  // The character first appears as contours, then a scan reveals the material.
  const edgeAlpha=scene*(1-smooth(1.8,2.65,t));
  ctx.save();ctx.globalAlpha=edgeAlpha;
  const scan=smooth(.6,2.0,t);
  ctx.beginPath();ctx.rect(0,0,W,H*scan);ctx.clip();
  ctx.drawImage(edges,dx,dy,W*zoom,H*zoom);ctx.restore();
  // Facial wire grid fades as the portrait resolves.
  ctx.save();ctx.globalAlpha=edgeAlpha*.27;
  for(let i=0;i<15;i++){
    const x=1160+i*43;
    line([[x-70,0],[x+18,340],[x-30,620],[x-150,1080]],'#75c8ff',1);
  }
  for(let i=0;i<18;i++){
    const y=i*61;
    line([[1020,y+30],[1360,y-15],[1750,y+25],[1920,y]],'#75c8ff',1);
  }
  ctx.restore();
  ctx.save();ctx.globalAlpha=color*scene;ctx.translate(dx,dy);ctx.scale(zoom,zoom);
  eyes.irisLight(opening,t);ctx.restore();
  // The left side stays quiet, so the title is readable over the portrait.
  const shade=ctx.createLinearGradient(0,0,W,0);
  shade.addColorStop(0,'rgba(0,3,7,.5)');shade.addColorStop(.36,'rgba(0,3,7,.22)');shade.addColorStop(.7,'rgba(0,3,7,0)');
  ctx.fillStyle=shade;ctx.fillRect(0,0,W,H);
  ctx.save();ctx.globalAlpha=scene*.52;
  for(let i=0;i<46;i++){
    const x=((Math.sin(i*67.1)*43758.54)%1+1)%1*W;
    const y=(((Math.sin(i*43.7)*12731.4)%1+1)%1*H-t*(3+i%6)+H)%H;
    const a=.15+.4*(.5+.5*Math.sin(t*1.6+i));
    ctx.fillStyle=`rgba(135,210,255,${a})`;ctx.beginPath();ctx.arc(x,y,i%9===0?2:1,0,Math.PI*2);ctx.fill();
  }
  ctx.restore();
  if(t<2.75){
    ctx.save();ctx.globalAlpha=(1-smooth(2.0,2.75,t))*scene*.5;
    const sy=H*smooth(.6,2.3,t);
    const glow=ctx.createLinearGradient(0,sy-14,0,sy+14);
    glow.addColorStop(0,'transparent');glow.addColorStop(.5,'rgba(113,212,255,.3)');glow.addColorStop(1,'transparent');
    ctx.fillStyle=glow;ctx.fillRect(960,sy-14,960,28);ctx.restore();
  }
  // Fine orbital strokes echo the reference's blue interface geometry.
  ctx.save();ctx.globalAlpha=scene*.6;
  ctx.strokeStyle='rgba(113,198,237,.32)';ctx.lineWidth=1;
  ctx.beginPath();ctx.ellipse(628,554,489,76,-.17,0,Math.PI*2);ctx.stroke();
  const angle=-.7+t*.19, px=628+489*Math.cos(angle),py=554+76*Math.sin(angle)-80*Math.cos(angle);
  const halo=ctx.createRadialGradient(px,py,0,px,py,18);
  halo.addColorStop(0,'rgba(232,251,255,.9)');halo.addColorStop(.15,'rgba(126,214,255,.7)');halo.addColorStop(1,'transparent');
  ctx.fillStyle=halo;ctx.fillRect(px-18,py-18,36,36);ctx.restore();
  // Small peripheral typography belongs to the film, not to the app UI.
  if(!isLogin){
  ctx.save();ctx.globalAlpha=scene*.66;
  text('AI EMBY',96,76,18,'#c0d9e6','"Segoe UI", sans-serif');
  line([[96,94],[430,94]],'rgba(163,202,221,.4)');
  text('PERSONAL CINEMA',96,123,10,'#708d9e');
  text('SEQUENCE / 001',1700,75,11,'#93b4c8');
  text(variant.label,1700,95,9,'#6b8d9f');
  line([[60,228],[60,465]],'rgba(153,198,222,.28)');
  line([[60,993],[203,993]],'rgba(153,198,222,.4)');
  text('YOUR STORIES. YOUR UNIVERSE.',60,1020,10,'#98b6c6');
  text('AI EMBY / CINEMA EXPERIENCE',1570,1019,10,'#91b0c0');
  ctx.restore();
  // Each letter is traced in sequence, then individually resolves into metal.
  // Settled letters stay in place while the next glyph appears.
  if(t>2.35) {
  ctx.save();ctx.translate(150,580);
  ctx.font='italic 900 140px "Segoe UI", Arial, sans-serif';ctx.lineJoin='round';
  ctx.strokeStyle='rgba(165,212,239,.65)';ctx.lineWidth=1.5;
  const lettering='AI EMBY';
  const metallic=ctx.createLinearGradient(0,-135,0,20);
  metallic.addColorStop(0,'#fbfdff');metallic.addColorStop(.31,'#dbe9f5');metallic.addColorStop(.5,'#8da3bc');metallic.addColorStop(.53,'#bed6ed');metallic.addColorStop(.74,'#f5fbff');metallic.addColorStop(1,'#66829b');
  ctx.fillStyle=metallic;
  let letterIndex=0;
  for(let i=0;i<lettering.length;i++) {
    const glyph=lettering[i];if(glyph===' ')continue;
    const start=2.35+letterIndex*.18;
    const trace=smooth(start,start+.16,t);
    const fill=smooth(3.25+letterIndex*.12,3.55+letterIndex*.12,t);
    letterIndex++;
    if(trace===0)continue;
    const x=ctx.measureText(lettering.slice(0,i)).width+8*(1-trace),y=6*(1-trace);
    ctx.shadowBlur=0;ctx.globalAlpha=trace;ctx.strokeText(glyph,x,y);
    if(fill>0){
      ctx.globalAlpha=trace*fill;ctx.shadowColor=`rgba(101,188,255,${.5*fill})`;ctx.shadowBlur=16;
      ctx.fillText(glyph,x,y);
    }
  }
  ctx.shadowBlur=0;ctx.globalAlpha=smooth(3.45,4.2,t);
  line([[0,33],[ctx.measureText(lettering).width,33]],'rgba(120,182,222,.35)');
  spaced(variant.subtitle,3,78,23,6,'#c0d7eb');
  spaced('Y O U R   P E R S O N A L   C I N E M A',4,109,11,1,'#6e9bb7');
  ctx.restore();
  }
  ctx.save();ctx.globalAlpha=scene*.6;
  const lines=isNailong?['> loading movie snacks','> trying to look serious','> welcome to AI EMBY']:['> creating your universe','> bringing stories to life','> welcome to AI EMBY'];
  const count=t<2?1:t<3.8?2:3;
  for(let i=0;i<count;i++)text(lines[i],158,770+i*23,12,'#6794a3');
  if(t<4.8&&Math.floor(t*3)%2===0){ctx.fillStyle='#77c5d6';ctx.fillRect(158,774+count*23,6,12);}
  ctx.restore();
  }
  const final=smooth(6.68,7.2,t);
  if(!isLogin&&final){ctx.fillStyle=`rgba(2,5,8,${final})`;ctx.fillRect(0,0,W,H);}
}
(async()=>{
  const open=await loadImage(path.join(output,variant.open));
  const closed=await loadImage(path.join(output,variant.closed));
  if(open.width!==closed.width||open.height!==closed.height)throw new Error('Eye keyframes must share the same dimensions.');
  const eyes=isNailong?makeNailongEyes(open,closed):makeEyeMotion(open,closed),edges=makeEdges(eyes.frame(0));
  const stillDir=renderArgs[2];if(stillDir)fs.mkdirSync(stillDir,{recursive:true});
  const stillFrames=[54,70,75,81,87,93,96,105,114,126,132,154,165,174];
  if(process.argv.includes('--stills-only')) {
    if(!stillDir)throw new Error('Supply a diagnostics directory for --stills-only.');
    for(const i of stillFrames){draw(eyes,edges,i/FPS);fs.writeFileSync(path.join(stillDir,`eye-${i}.png`),canvas.toBuffer('image/png'));}
    console.log('Eye motion stills saved.');return;
  }
  const ffmpeg=spawn(renderArgs[1] || 'ffmpeg',['-y','-loglevel','error','-f','rawvideo','-pixel_format','rgba','-video_size',`${W*OUTPUT_SCALE}x${H*OUTPUT_SCALE}`,'-framerate',String(FPS),'-i','pipe:0','-an','-c:v','libx264','-preset','medium','-crf','20','-pix_fmt','yuv420p','-movflags','+faststart',path.join(output,variant.film)],{stdio:['pipe','ignore','inherit']});
  const finished=once(ffmpeg,'close');
  for(let i=0;i<Math.round(FPS*DURATION);i++){
    draw(eyes,edges,i/FPS);
    if(i===(isNailong?174:165))fs.writeFileSync(path.join(output,variant.poster),canvas.toBuffer('image/jpeg'));
    if(stillDir&&stillFrames.includes(i))fs.writeFileSync(path.join(stillDir,`eye-${i}.png`),canvas.toBuffer('image/png'));
    if(!ffmpeg.stdin.write(Buffer.from(ctx.getImageData(0,0,W*OUTPUT_SCALE,H*OUTPUT_SCALE).data)))await once(ffmpeg.stdin,'drain');
    if(i%60===0)console.log(`Rendered ${i}/${Math.round(FPS*DURATION)} frames`);
  }
  ffmpeg.stdin.end();
  const [code]=await finished;if(code!==0)throw new Error(`ffmpeg exited ${code}`);
  console.log('Opening film and poster saved to frontend/web/assets.');
})().catch(error=>{console.error(error);process.exitCode=1;});
