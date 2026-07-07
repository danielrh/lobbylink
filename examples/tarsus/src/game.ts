/*
Copyright (c) 2024 by the Alexander Reiter Horn and Albert Sung.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.  IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

import { P2PGame } from "./p2p-client.js";
import type { P2PEvent } from "./p2p-client.js";
import * as P from "./protocol.js";

////////////// Setup the Window //////////////////
const canvas = document.createElement("canvas");
document.body.style.overflow="hidden"; // hide scroll bars
canvas.id = "drawArea";
canvas.setAttribute("style", "background-color:#000008;bottom:2px;right:2px;z-index:100;position:fixed");
const origWidth: number = 640;
const origHeight: number = 480;
canvas.width = origWidth; // 640;
canvas.height = origHeight; // 480;
canvas.tabIndex = 0;
const ctx = canvas.getContext("2d")!;

const existingCanvas = document.getElementById(canvas.id);
if (existingCanvas && existingCanvas.parentElement) {
  existingCanvas.parentElement.removeChild(existingCanvas);
}
document.body.appendChild(canvas);
canvas.focus()
const K_SCALE:number = .0025;
// Pirates only open fire when the target could plausibly see them (just past
// the 640x480 screen corner) and lose interest entirely at long range.
const NPC_SHOOT_RANGE = 500;
const NPC_DISENGAGE_RANGE = 3000;
// Press L within this distance of a base to dock (repair + invulnerable).
const DOCK_RANGE = 170;
const boom_urls:string[] = ["https://graphics.stanford.edu/~danielh/sprites/boom/01.png","https://graphics.stanford.edu/~danielh/sprites/boom/02.png","https://graphics.stanford.edu/~danielh/sprites/boom/03.png","https://graphics.stanford.edu/~danielh/sprites/boom/04.png","https://graphics.stanford.edu/~danielh/sprites/boom/05.png","https://graphics.stanford.edu/~danielh/sprites/boom/06.png","https://graphics.stanford.edu/~danielh/sprites/boom/07.png","https://graphics.stanford.edu/~danielh/sprites/boom/08.png","https://graphics.stanford.edu/~danielh/sprites/boom/09.png","https://graphics.stanford.edu/~danielh/sprites/boom/10.png","https://graphics.stanford.edu/~danielh/sprites/boom/11.png","https://graphics.stanford.edu/~danielh/sprites/boom/12.png","https://graphics.stanford.edu/~danielh/sprites/boom/13.png","https://graphics.stanford.edu/~danielh/sprites/boom/14.png",]
function square(x:number) {
  return x*x;
}

function dist(x:number,y:number,i:number,j:number){
  return Math.sqrt(square(x-i)+square(y-j))
}
function popListPosition(x:any[],position:number) {
  x[position] = x[x.length-1]
  x.pop()
  return(x)
}
function popList2Position(x:any[][],position:number) {
  for(let i = 0; i<x.length;i++) {
    let lastIndex = x[i].length-1;
    x[i][position] = x[i][lastIndex]
    x[i].pop()
  }

}
function tick(){
  
}



/////////////// The camera /////////////////////
class Camera {
  cameraX:number[] = [0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]
  cameraY:number[] = [0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]
  public update (cameraX:number,cameraY:number){
    this.cameraX.push(cameraX)
    this.cameraY.push(cameraY)
    this.cameraX.shift()
    this.cameraY.shift()
  }

}
////////////////// stars ///////////////////////
class Stars {
  starsX:number[] = []
  starsY:number[] = []
  sprite: HTMLImageElement[] = [];
size: number;
  public createstar () {
    var x = Math.round(Math.random() * canvas.width)+camera.cameraX[0]
    var y = Math.round(Math.random() * canvas.height)+camera.cameraY[0]
    this.starsX.push(x)
    this.starsY.push(y)
  }
  constructor(imageUrl:string[]) {
      this.size = 0.75;
      for (var i = 0; i < imageUrl.length;i++) {
        let image = new Image();
        image.src = imageUrl[i];
        this.sprite.push(image);
      }
      for(var i =0; i < 100 / 640 / 480 * canvas.width * canvas.height;i++)
      {
        this.createstar()
      }
    }
    public draw(camera:Camera) {
      for (var i = 0;
      i<this.starsX.length;
      i++) 
      {
        ctx.save();
        var x = this.starsX[i]-camera.cameraX[0]+canvas.width/2
        var y = this.starsY[i]-camera.cameraY[0]+canvas.height/2
        x=x%canvas.width
        y=y%canvas.height
        if (x <0) {
          x = x + canvas.width;
        }
        if (y <0) {
          y = y + canvas.height;
        }
        ctx.translate(x,y);
        const spriteIndex = i%this.sprite.length;
        ctx.drawImage(this.sprite[spriteIndex], 0, 0, this.sprite[spriteIndex].width, this.sprite[spriteIndex].height,
                  -this.sprite[spriteIndex].width/2*this.size, -this.sprite[spriteIndex].height/2*this.size, this.sprite[spriteIndex].width*this.size, this.sprite[spriteIndex].height*this.size);
        ctx.restore();
      }
  }
}

class Asteroids {
  asteroidsX:number[] = []
  asteroidsY:number[] = [] 
  sprite: HTMLImageElement;
size: number;
  public createasteroid() {
  var x = Math.round(Math.random() * canvas.width*2)+camera.cameraX[0]
  var y = Math.round(Math.random() * canvas.height*2)+camera.cameraY[0]
  this.asteroidsX.push(x);
  this.asteroidsY.push(y);

  }
constructor(imageURL: string) {
  this.size = 1;
  let image = new Image();
  image.src = imageURL;
  this.sprite = image;
    for(var i = 0; i < 15; i++)
   {
     this.createasteroid()
   }
}
  public draw(camera: Camera) {
  for (var i = 0;
  i<this.asteroidsX.length;
  i++)
  {
    ctx.save();
    var x = this.asteroidsX[i]-camera.cameraX[0]+canvas.width/2
    var y = this.asteroidsY[i]-camera.cameraY[0]+canvas.height/2
    if (x < -canvas.width/2){
      this.asteroidsX[i] += canvas.width*2;
    }
     if (y < -canvas.height/2){
      this.asteroidsY[i] += canvas.height*2;
    }
     if (x > 3*canvas.width/2){
      this.asteroidsX[i] -= canvas.width*2;
    }
     if (y > 3*canvas.height/2){
      this.asteroidsY[i] -= canvas.height*2;
    }
    ctx.translate(x,y);
    ctx.drawImage(this.sprite, 0,0, this.sprite.width, this.sprite.height,
                  -this.sprite.width/2*this.size,this.sprite.height*this.size,
                  this.sprite.height*this.size, this.sprite.height*this.size);
    ctx.restore();
  }
}
}
//boomsprites"https://graphics.stanford.edu/~danielh/sprites/boom/01.png"
class Boom {
  boomX:    number[] = []
  boomY:    number[] = []
  boomFrame:number[] = []
  size:number = 1;
  boomImages:HTMLImageElement[] = []
  
  constructor(spriteUrls:string[]){
    for (var i = 0; i < spriteUrls.length;i++) {
      let image = new Image();
      image.src = spriteUrls[i];
      this.boomImages.push(image);
    }
  }
  public createBoom(x:number,y:number) {
    this.boomX.push(x)
    this.boomY.push(y)
    this.boomFrame.push(0)
  }
  public update() {
    for(var i = 0; this.boomX.length > i;i++) {
      this.boomFrame[i]++
      if (this.boomFrame[i]>=this.boomImages.length) {
        popList2Position([this.boomX,this.boomY,this.boomFrame],i);
        i--;
      }
    }
  }
  public draw(camera:Camera) {
    for(var i = 0; this.boomX.length > i;i++) {
      ctx.save();
      ctx.translate(this.boomX[i]-camera.cameraX[0]+canvas.width/2,this.boomY[i]-camera.cameraY[0]+canvas.height/2);
      ctx.drawImage(this.boomImages[this.boomFrame[i]], 0, 0, this.boomImages[this.boomFrame[i]].width, this.boomImages[this.boomFrame[i]].height,
                    -this.boomImages[this.boomFrame[i]].width/2*this.size, -this.boomImages[this.boomFrame[i]].height/2*this.size, this.boomImages[this.boomFrame[i]].width*this.size, this.boomImages[this.boomFrame[i]].height*this.size);
      ctx.restore();
    }
  }
}
/////////// lazzzzers!!! ///////////
// Every laser gets an id unique to this client so peers can refer to it in
// damage messages (and so a hit is only counted once).
let laserIdCounter = 0;
function nextLaserId(): number {
  laserIdCounter = (laserIdCounter + 1) & 0xffff;
  return laserIdCounter;
}
class Lasers {
  laserX:     number[] = []
  laserY:     number[] = []
  laserStartX:number[] = []
  laserStartY:number[] = []
  laserId:    number[] = []
  laserDamage:number = 5
  velocity:   number = 5500 * K_SCALE
  damageArea2:number = 8.875
  damageArea1:number = 17.75*3
  laserVelocityX:number[] = []
  laserVelocityY:number[] = []
  laserEntityStartId:Ship[] = []
  allProperties:any[][]
  sprite: HTMLImageElement[] = [];
  size: number;
  public createlaser (entity: Ship, x:number, y:number, xVelocity:number, yVelocity:number) {

    this.laserX.push(x)
    this.laserY.push(y)
    this.laserVelocityX.push(xVelocity)
    this.laserVelocityY.push(yVelocity)
    this.laserStartX.push(x)
    this.laserStartY.push(y)
    this.laserId.push(nextLaserId())
    this.laserEntityStartId.push(entity);

  }
  constructor(imageUrl:string[]) {
    this.size = 0.25;
    this.allProperties = [this.laserX, this.laserY, this.laserVelocityX, this.laserVelocityY, this.laserStartX, this.laserStartY, this.laserId, this.laserEntityStartId];
    for (var i = 0; i < imageUrl.length;i++) {
      let image = new Image();
      image.src = imageUrl[i];
      this.sprite.push(image);
    }
  }

  public update() {
    for (var i = this.laserX.length - 1; i>=0; i--) {
      if (dist(this.laserStartX[i],this.laserStartY[i],this.laserX[i],this.laserY[i])>3500) {
        let lastIndex = this.laserX.length - 1;
        for(var j = 0;j<this.allProperties.length;j++){
          this.allProperties[j][i] = this.allProperties[j][lastIndex];
          this.allProperties[j].pop();
        }
      }
    }
    for (var i = 0;i<this.laserX.length;i++) {
      this.laserX[i] += this.laserVelocityX[i]
      this.laserY[i] += this.laserVelocityY[i]
    }
  }

  public draw(camera:Camera) {
    for (var i = 0;i<this.laserX.length;i++) {
      ctx.save();
      var x = this.laserX[i]-camera.cameraX[0]+canvas.width/2
      var y = this.laserY[i]-camera.cameraY[0]+canvas.height/2
      ctx.translate(x,y);
      const spriteIndex = i%this.sprite.length;
      ctx.drawImage(this.sprite[spriteIndex], 0, 0, this.sprite[spriteIndex].width, this.sprite[spriteIndex].height,
              -this.sprite[spriteIndex].width/2*this.size, -this.sprite[spriteIndex].height/2*this.size, this.sprite[spriteIndex].width*this.size, this.sprite[spriteIndex].height*this.size);
      ctx.restore();
    }
  }
}
function toRadians(degrees: number) {
  return degrees * Math.PI / 180;
}
//////////// Setup the Ship///////////////////
class Ship {
  lastShot:                number = 0
  shotEnergyCost:          number = 10 
  maxSetSpeed:             number = 113470/64*K_SCALE
  maxAfterburnerVelocity:  number = 164200/64*K_SCALE
  afterburnerAcceleration: number = 100
  acceleration:            number = 80
  setSpeed:                number = 0
  maxEnergy:               number = 300
  energy:                  number = 300
  energyRegeneration:      number = 2
  xVelocity:               number = 0
  yVelocity:               number = 0
  size:                    number = 0.75;
  shieldRegeneration:      number = 1/30
  shieldRegenerationcost:  number = 1/20
  shieldsMax:              number = 200
  shields:                 number = 200
  spacePressed: boolean = false;
  tabPressed:   boolean = false;
  upPressed:    boolean = false;
  downPressed:  boolean = false;
  leftPressed:  boolean = false;
  rightPressed: boolean = false;
  alive: boolean = true;  // only ever false for the local player in multiplayer
  docked: boolean = false; // parked at a base: invulnerable, repairing (local player only)
  npcId: number = 0;      // wire id when this ship is a host-simulated pirate
  npcSprite: number = 0;  // index into P.PIRATE_SPRITES when this is a pirate
  speedFactor: number = 1; // pirate cruise-speed multiplier (varies by hull)
  lastHitBy: Ship|null = null; // owner of the last laser that hit us (bounty credit)
  x: number  = 320;
  y: number = 240;
  shieldTime:number=0;
  angle: number = 0;
  angleSpeed: number = 0;
  sprite: HTMLImageElement;
  spriteShield:HTMLImageElement;

  constructor(imageUrl:string,x:number|undefined,y:number|undefined) {
    let image = new Image();
    image.src = imageUrl;
    this.sprite = image;
    image = new Image();
    image.src = "https://graphics.stanford.edu/~danielh/sprites/shield.png";
    this.spriteShield = image;
    if (x !== undefined) {
      this.x = x
    }
    if (y !== undefined) {
      this.y = y
    }
  }
  public curMaxSpeed() {
    if (this.tabPressed) {
      return this.maxAfterburnerVelocity;
    }else {
      return this.maxSetSpeed;
    }
  }
  public getSpeed() {
   return Math.sqrt((this.xVelocity*this.xVelocity)+(this.yVelocity*this.yVelocity)) 
  }
  public getDirectionX(){
    return Math.cos(toRadians(this.angle));
  }
  public getDirectionY(){
    return Math.sin(toRadians(this.angle));
  }
  public getLeftDirectionX(){
    return Math.cos(toRadians(this.angle + 90));
  }
  public getLeftDirectionY(){
    return Math.sin(toRadians(this.angle + 90));
  }
  public getRightDirectionX(){
    return Math.cos(toRadians(this.angle - 90));
  }
  public getRightDirectionY(){
    return Math.sin(toRadians(this.angle - 90));
  }
  public getBackDirectionX(){
    return Math.cos(toRadians(this.angle - 180));
  }
  public getBackDirectionY(){
    return Math.sin(toRadians(this.angle - 180));
  }
  public draw(camera:Camera) {

    ctx.save();
    ctx.translate(this.x-camera.cameraX[0]+canvas.width/2,this.y-camera.cameraY[0]+canvas.height/2);
    ctx.rotate(Math.PI/2)
    ctx.rotate(toRadians(this.angle));
    ctx.drawImage(this.sprite, 0, 0, this.sprite.width, this.sprite.height,
                  -this.sprite.width/2*this.size, -this.sprite.height/2*this.size, this.sprite.width*this.size, this.sprite.height*this.size);
    let sizeShield = this.size/2
    if (this.shieldTime>0&&(this.shieldTime%3==1)) {
      ctx.drawImage(this.spriteShield, 0, 0, this.spriteShield.width, this.spriteShield.height,
                  -this.spriteShield.width/2*sizeShield, -this.spriteShield.height/2*sizeShield, this.spriteShield.width*sizeShield, this.spriteShield.height*sizeShield);
    }
    ctx.restore();
  }
  public update(){
    this.shieldTime--
    let isAlive = true
    if (this.getSpeed() >= this.curMaxSpeed()) {
      let ratio = this.getSpeed() / this.curMaxSpeed();
      //((player().getSpeed() - player().curMaxSpeed()) / 1.01) + player().curMaxSpeed()
      this.xVelocity /= ratio;
      this.yVelocity /= ratio;
    }
    this.energy+=this.energyRegeneration
    if(this.energy<0) {
      this.energy=0
    }
    if(this.energy>this.maxEnergy) {
      this.energy=this.maxEnergy
    }
    if(this.energy>=this.shieldRegenerationcost&&this.shields<this.shieldsMax) {
      this.energy -= this.shieldRegenerationcost
      this.shields += this.shieldRegeneration
    }
    if(this.shields>this.shieldsMax) {
      this.shields=this.shieldsMax
    }
    this.x += this.xVelocity
    this.y += this.yVelocity                      
    this.angle += this.angleSpeed                
    for(var i = laser.laserX.length-1; i >= 0 ; i--)
    {
      if (dist(laser.laserX[i],laser.laserY[i],this.x,this.y)>=laser.damageArea2 && dist(laser.laserX[i],laser.laserY[i],this.x,this.y)<laser.damageArea1 && laser.laserEntityStartId[i]!== this) {
        this.shields -= 10
        this.shieldTime=20
        this.lastHitBy = laser.laserEntityStartId[i]
        if( 0 >= this.shields) {
          isAlive = false
          
        }
        popList2Position(laser.allProperties,i)
        
      }
    }
    if(isAlive===false){
      boom.createBoom(this.x,this.y)
    }
    return isAlive
  }
  public ai(targetX:number, targetY:number) {
    const targetDist = dist(targetX, targetY, this.x, this.y);
    if (targetDist < NPC_DISENGAGE_RANGE) {
      let targetAngle = (Math.atan2(targetY-this.y,targetX-this.x)*180/Math.PI + 360) % 360;
      //let targetAngle2 = targetAngle+360
      let myAngle = (this.angle +360) % 360;
      //let myAngle2 = myAngle + 360
      let diff = targetAngle - myAngle;
      if (diff >= 180) {
          diff -= 360;
      }
      if (diff < -180) {
          diff += 360;
      }
      if (diff > 0) {
        this.angle += 1;
      } else {
        this.angle -= 1;
      }
      if (targetDist < NPC_SHOOT_RANGE) {
        shoot(this)
      }
    }
    this.xVelocity = this.maxSetSpeed * Math.cos(toRadians(this.angle)) *.1 * this.speedFactor;
    this.yVelocity = this.maxSetSpeed * Math.sin(toRadians(this.angle)) *.1 * this.speedFactor;
  }
}
let camera = new Camera();
function player() {
  return ships[0]
}
let ships   =[new Ship("https://graphics.stanford.edu/~danielh/sprites/Orion.png", 320,240), new Ship("https://graphics.stanford.edu/~danielh/sprites/Talon_-_Pirate.png",300,120),new Ship("https://graphics.stanford.edu/~danielh/sprites/Talon_-_Pirate.png",400,320)];
let boom   = new Boom(boom_urls)
let laser  = new Lasers(["https://graphics.stanford.edu/~danielh/sprites/BallRed.png"])
let asteroids = new Asteroids("https://graphics.stanford.edu/~danielh/sprites/Asteroid1.png")
let starList = ["https://graphics.stanford.edu/~danielh/sprites/Star2.png","https://graphics.stanford.edu/~danielh/sprites/Star2.png","https://graphics.stanford.edu/~danielh/sprites/Star2.png","https://graphics.stanford.edu/~danielh/sprites/Star2.png","https://graphics.stanford.edu/~danielh/sprites/Star5.png","https://graphics.stanford.edu/~danielh/sprites/Star3.png","https://graphics.stanford.edu/~danielh/sprites/Star3.png"]
let stars  = new Stars(starList);
////////////////// seeded landmarks /////////////////////
// Same fixed seed on every client, so the bases are in the same place for
// everyone with zero network traffic.
const bases = P.generateBases();
const baseSprites = P.BASE_SPRITES.map((name) => {
  const img = new Image();
  img.src = P.SPRITE_BASE + name;
  return img;
});
//////////////// Handle Keyboard ///////////////////////
canvas.onkeydown = function(e) {
  if (e.key == "a" || e.key == "ArrowLeft") {
    player().leftPressed = true
  }
  if (e.key == "d" || e.key == "ArrowRight") {
    player().rightPressed = true
  }
  if (e.key == "w" || e.key == "ArrowUp") {
    player().upPressed = true
  }
  if (e.key == "s" || e.key == "ArrowDown") {
    player().downPressed = true
  }
  if (e.key == "Tab") {
    player().tabPressed = true
  }
  if (e.key == " ") {
    player().spacePressed = true
  }
  
  e.preventDefault();
};

function changeCanvasSize() {
  if (canvas.width == origWidth) {
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
  } else {
    canvas.width = origWidth;
    canvas.height = origHeight;
  }
  stars = new Stars(starList);
}

canvas.onkeyup = function(e) {
  if (e.key == "a" || e.key == "ArrowLeft") {
    player().leftPressed = false
  }
  if (e.key == "d" || e.key == "ArrowRight") {
    player().rightPressed = false
  }
  if (e.key == "w" || e.key == "ArrowUp") {
    player().upPressed = false
  }
  if (e.key == "s" || e.key == "ArrowDown") {
    player().downPressed = false
  }
  if (e.key == "Tab") {
    player().tabPressed = false
  }
  if (e.key == " ") {
    player().spacePressed = false
  }
  if (e.key == "l" || e.key == "L") {
    toggleDock()
  }
  if (e.key == "F10") {
    if (canvas.width == origWidth) {
      canvas.requestFullscreen().then(changeCanvasSize).catch(changeCanvasSize);
    } else {
      changeCanvasSize();
      document.exitFullscreen();
    }
  }
  if (e.key == "F11") {
    if (canvas.style.height != window.innerHeight + "px") {
      canvas.requestFullscreen();
      //canvas.style.width = window.innerWidth + "px";
      canvas.style.height = window.innerHeight + "px";
    } else {
      //canvas.style.width = "";
      canvas.style.height = "";
    }
  }
  e.preventDefault();
};

function shoot(ship:Ship) {
  if(Date.now()-ship.lastShot>275 && ship.energy>=ship.shotEnergyCost) {
    ship.energy -= ship.shotEnergyCost
    let xVelocity = ship.xVelocity+(laser.velocity*(Math.cos(ship.angle*Math.PI/180)));
    let yVelocity = ship.yVelocity+(laser.velocity*(Math.sin(ship.angle*Math.PI/180)));
  laser.createlaser(ship, ship.x+ship.getRightDirectionX()*10,ship.y+ship.getRightDirectionY()*10,xVelocity,yVelocity)
  laser.createlaser(ship, ship.x+ship.getLeftDirectionX()*10,ship.y+ship.getLeftDirectionY()*10,xVelocity,yVelocity)
  ship.lastShot = Date.now()
  }
  
}
////////////////// multiplayer (lobbylink) //////////////////
// Trust model mirrors DartRoids (../../PROTOCOL.md): everyone simulates and
// broadcasts their own ship + lasers best-effort; the *victim* of a laser hit
// is authoritative for its own shields and reports the hit on the reliable
// channel; the lowest-id connected player hosts the NPC pirates.

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const hud = $("hud");
const toastEl = $("toast");
const mpBtn = $("mp");
const mpMenu = $("mpMenu");

let net: P2PGame | null = null;
let selfSlot = 0;
let myName = "pilot";
let myShipIdx = 0;
let sendSeq = 0;
let npcSeq = 0;
let sendAcc = 0;
let joinedAt = 0;
let wasHost = false;
let deadUntil = 0;
let nextNpcId = 1;
let nextPirateSpawnAt = 0;
let toastUntil = 0;
let myScore = 0; // kills + pirate bounties; broadcast in STATE, kept client-side
let dockedBase: P.Base | null = null;
const NPC_TARGET_COUNT = 2;
// Index-aligned with P.PIRATE_SPRITES: speed scales the cruise velocity and
// heavier hulls carry more shields.
const PIRATE_STATS = [
  { speed: 1.0,  shields: 200 }, // Talon - Pirate
  { speed: 1.1,  shields: 160 }, // Talon - Retro
  { speed: 1.35, shields: 120 }, // Demon: fast and fragile
  { speed: 0.9,  shields: 260 }, // Gothri
  { speed: 1.15, shields: 150 }, // Dralthi
  { speed: 0.6,  shields: 420 }, // Kamekh: slow gunboat
];
function pirateSprite(idx: number): string {
  return P.SPRITE_BASE + P.PIRATE_SPRITES[idx % P.PIRATE_SPRITES.length];
}

interface RemoteEntity {
  ship: Ship; // pose/sprite holder so Ship.draw renders it
  x: number; y: number; angle: number; angleSpeed: number;
  vx: number; vy: number;
  px: number; py: number; pa: number; // eased render pose
  shields: number;
  lastRecv: number;
}
interface RemotePlayer extends RemoteEntity {
  name: string;
  shipIdx: number;
  alive: boolean;
  docked: boolean;
  score: number;
  lasers: P.WireLaser[];
  seq: number;
}
interface RemoteNpc extends RemoteEntity {
  sprite: number;
}

const remotes = new Map<number, RemotePlayer>();
const remoteNpcs = new Map<number, RemoteNpc>();
let npcLasers: P.WireLaser[] = [];
let npcLasersFrom = -1; // slot the NPC broadcast came from (the host)
let lastNpcRecv = 0;
let npcStateSeq = 0;
let npcStateHasSeq = false;

// Lasers that already hit something, keyed by (owner slot, laser id): never
// damage, and never draw, the same laser twice.
const consumedLasers = new Set<number>();
function laserKey(slot: number, id: number): number {
  return slot * 0x10000 + id;
}
function consumeLaser(slot: number, id: number): void {
  consumedLasers.add(laserKey(slot, id));
  if (consumedLasers.size > 512) {
    for (const k of consumedLasers) {
      consumedLasers.delete(k);
      if (consumedLasers.size <= 256) break;
    }
  }
}

function netActive(): boolean {
  return net !== null;
}
function hostSlot(): number {
  if (!net) return -1;
  let low = selfSlot;
  for (const p of net.players) {
    if (p.occupied && p.connected && p.id < low) low = p.id;
  }
  return low;
}
function isHost(): boolean {
  return netActive() && hostSlot() === selfSlot;
}

function broadcastReliable(bytes: Uint8Array): void {
  if (!net) return;
  for (const p of net.players) {
    if (p.occupied && p.id !== selfSlot) net.sendReliable(p.id, bytes).catch(() => {});
  }
}
function broadcastDamage(d: Omit<P.DamageMsg, "kind">): void {
  broadcastReliable(P.encodeDamage(d));
}

function myWireLasers(): P.WireLaser[] {
  const out: P.WireLaser[] = [];
  for (let i = 0; i < laser.laserX.length; i++) {
    if (laser.laserEntityStartId[i] !== player()) continue;
    out.push({ id: laser.laserId[i], x: laser.laserX[i], y: laser.laserY[i], vx: laser.laserVelocityX[i], vy: laser.laserVelocityY[i] });
  }
  return out;
}
function npcWireLasers(): P.WireLaser[] {
  const out: P.WireLaser[] = [];
  for (let i = 0; i < laser.laserX.length; i++) {
    if (laser.laserEntityStartId[i] === player()) continue;
    out.push({ id: laser.laserId[i], x: laser.laserX[i], y: laser.laserY[i], vx: laser.laserVelocityX[i], vy: laser.laserVelocityY[i] });
  }
  return out;
}

function dieMp(shooterSlot: number, laserId: number, npcLaser: boolean, boomAlready: boolean): void {
  const p = player();
  if (!p.alive) return;
  p.alive = false;
  deadUntil = performance.now() + P.RESPAWN_DELAY_MS;
  if (!boomAlready) boom.createBoom(p.x, p.y);
  broadcastDamage({ died: true, npcLaser, shooterSlot, laserId, shieldsAfter: 0, x: p.x, y: p.y });
  showToast("your ship was destroyed — respawning…");
}

/** The nearest base within docking distance of the player, if any. */
function dockableBase(): P.Base | null {
  const p = player();
  let best: P.Base | null = null;
  let bestD = Infinity;
  for (const b of bases) {
    const d = dist(b.x, b.y, p.x, p.y);
    if (d < bestD) {
      bestD = d;
      best = b;
    }
  }
  return bestD <= DOCK_RANGE ? best : null;
}

function toggleDock(): void {
  const p = player();
  if (!p.alive) return;
  if (p.docked) {
    p.docked = false;
    dockedBase = null;
    showToast("launched — good hunting");
    return;
  }
  const b = dockableBase();
  if (b) {
    p.docked = true;
    dockedBase = b;
    p.xVelocity = 0;
    p.yVelocity = 0;
    p.angleSpeed = 0;
    p.shieldTime = 0;
    showToast(`docked at ${b.name} — repairing`);
  }
}

function respawnMp(): void {
  const p = player();
  p.alive = true;
  p.docked = false;
  dockedBase = null;
  p.shields = p.shieldsMax;
  p.energy = p.maxEnergy;
  p.shieldTime = 0;
  p.xVelocity = 0;
  p.yVelocity = 0;
  p.angleSpeed = 0;
  // come back near a landmark so lost pilots can regroup ("meet at Refinery 4")
  const b = bases[Math.floor(Math.random() * bases.length)];
  const ang = Math.random() * Math.PI * 2;
  const d = 150 + Math.random() * 250;
  p.x = b.x + Math.cos(ang) * d;
  p.y = b.y + Math.sin(ang) * d;
}

/** A remote laser hit me: I am authoritative, so damage myself and tell the room. */
function applyRemoteHit(shooterSlot: number, laserId: number, npcLaser: boolean): void {
  const p = player();
  p.shields -= P.LASER_DAMAGE;
  p.shieldTime = 20;
  if (p.shields <= 0) {
    dieMp(shooterSlot, laserId, npcLaser, false);
  } else {
    broadcastDamage({ died: false, npcLaser, shooterSlot, laserId, shieldsAfter: p.shields, x: p.x, y: p.y });
  }
}

/** Fixed-rate networking work: collisions between wire lasers and local ships. */
function netStep(): void {
  if (!net) return;
  const now = performance.now();
  const p = player();
  for (const r of remotes.values()) if (r.ship.shieldTime > 0) r.ship.shieldTime--;
  for (const n of remoteNpcs.values()) if (n.ship.shieldTime > 0) n.ship.shieldTime--;

  // remote players' lasers vs my ship (docked ships are safe in the hangar)
  if (p.alive && !p.docked) {
    outer: for (const [slot, r] of remotes) {
      const age = Math.min((now - r.lastRecv) / 1000, 0.4);
      for (const l of r.lasers) {
        if (consumedLasers.has(laserKey(slot, l.id))) continue;
        const d = dist(l.x + l.vx * 60 * age, l.y + l.vy * 60 * age, p.x, p.y);
        if (d >= laser.damageArea2 && d < laser.damageArea1) {
          consumeLaser(slot, l.id);
          applyRemoteHit(slot, l.id, false);
          if (!p.alive) break outer;
        }
      }
    }
  }
  // the host's pirate lasers vs my ship (the host's own copy lives in its
  // local pool and is handled by Ship.update there)
  if (p.alive && !p.docked && npcLasersFrom >= 0 && npcLasersFrom !== selfSlot) {
    const age = Math.min((now - lastNpcRecv) / 1000, 0.4);
    for (const l of npcLasers) {
      if (consumedLasers.has(laserKey(npcLasersFrom, l.id))) continue;
      const d = dist(l.x + l.vx * 60 * age, l.y + l.vy * 60 * age, p.x, p.y);
      if (d >= laser.damageArea2 && d < laser.damageArea1) {
        consumeLaser(npcLasersFrom, l.id);
        applyRemoteHit(npcLasersFrom, l.id, true);
        if (!p.alive) break;
      }
    }
  }

  // My lasers vs the host's NPCs: retire and report the hit reliably — the
  // report IS the damage path, so it fires on contact, however fresh the shot.
  // My lasers vs remote ships: purely cosmetic retirement, and only once the
  // laser has flown long enough (> one send interval) to have been broadcast;
  // eating it at birth would mean the victim — the damage authority — never
  // receives it, making point-blank shots do nothing.
  for (let i = laser.laserX.length - 1; i >= 0; i--) {
    if (laser.laserEntityStartId[i] !== p) continue;
    const lx = laser.laserX[i], ly = laser.laserY[i];
    let hit = false;
    if (!isHost()) {
      for (const [id, n] of remoteNpcs) {
        const d = dist(lx, ly, n.px, n.py);
        if (d >= laser.damageArea2 && d < laser.damageArea1) {
          hit = true;
          n.ship.shieldTime = 20; // predict the flash; the host applies the damage
          const h = hostSlot();
          if (h >= 0 && h !== selfSlot) net.sendReliable(h, P.encodeNpcDamage(id, P.LASER_DAMAGE)).catch(() => {});
          break;
        }
      }
    }
    if (!hit && dist(laser.laserStartX[i], laser.laserStartY[i], lx, ly) > 100) {
      for (const r of remotes.values()) {
        if (!r.alive || r.docked) continue;
        const d = dist(lx, ly, r.px, r.py);
        if (d >= laser.damageArea2 && d < laser.damageArea1) { hit = true; break; }
      }
    }
    if (hit) popList2Position(laser.allProperties, i);
  }
}

function shortestDeg(d: number): number {
  return ((d % 360) + 540) % 360 - 180;
}
/** Dead-reckon to the latest snapshot and ease the rendered pose toward it. */
function easeRemote(r: RemoteEntity, dt: number, now: number): void {
  const age = Math.min((now - r.lastRecv) / 1000, 0.4);
  const tx = r.x + r.vx * 60 * age;
  const ty = r.y + r.vy * 60 * age;
  const ta = r.angle + r.angleSpeed * 60 * age;
  const k = Math.min(1, dt * 12);
  r.px += (tx - r.px) * k;
  r.py += (ty - r.py) * k;
  r.pa += shortestDeg(ta - r.pa) * k;
}

function spawnPirate(): void {
  const anchors: { x: number; y: number }[] = [];
  if (player().alive) anchors.push({ x: player().x, y: player().y });
  for (const r of remotes.values()) if (r.alive) anchors.push({ x: r.px, y: r.py });
  const anchor = anchors.length > 0 ? anchors[Math.floor(Math.random() * anchors.length)] : { x: player().x, y: player().y };
  const ang = Math.random() * Math.PI * 2;
  const d = 500 + Math.random() * 400;
  const idx = Math.floor(Math.random() * P.PIRATE_SPRITES.length);
  const stats = PIRATE_STATS[idx % PIRATE_STATS.length];
  const pirate = new Ship(pirateSprite(idx), anchor.x + Math.cos(ang) * d, anchor.y + Math.sin(ang) * d);
  pirate.npcId = nextNpcId++ & 0xff;
  pirate.npcSprite = idx;
  pirate.speedFactor = stats.speed;
  pirate.shieldsMax = stats.shields;
  pirate.shields = stats.shields;
  ships.push(pirate);
}

/** I am now the lowest-id player: adopt the NPCs I last saw (or spawn fresh). */
function becomeHost(now: number): void {
  for (const [id, n] of remoteNpcs) {
    const stats = PIRATE_STATS[n.sprite % PIRATE_STATS.length];
    const pirate = new Ship(pirateSprite(n.sprite), n.px, n.py);
    pirate.npcId = id;
    pirate.npcSprite = n.sprite;
    pirate.speedFactor = stats.speed;
    pirate.shieldsMax = stats.shields;
    pirate.angle = n.pa;
    pirate.xVelocity = n.vx;
    pirate.yVelocity = n.vy;
    pirate.shields = n.shields;
    ships.push(pirate);
    if (id >= nextNpcId) nextNpcId = id + 1;
  }
  remoteNpcs.clear();
  npcLasers = [];
  while (ships.length - 1 < NPC_TARGET_COUNT) spawnPirate();
  nextPirateSpawnAt = now + 10000;
  showToast("you are hosting the pirates now");
}

/** A lower-id player owns the NPCs now: drop my local pirates and their lasers. */
function resignHost(): void {
  ships.splice(1);
  for (let i = laser.laserX.length - 1; i >= 0; i--) {
    if (laser.laserEntityStartId[i] !== player()) popList2Position(laser.allProperties, i);
  }
}

function sendState(): void {
  if (!net) return;
  const pl = player();
  net.broadcastBestEffort(P.encodeState({
    seq: sendSeq++ & 0xffff,
    alive: pl.alive,
    flash: pl.shieldTime > 0,
    docked: pl.docked,
    x: pl.x, y: pl.y,
    angle: pl.angle, angleSpeed: pl.angleSpeed,
    vx: pl.xVelocity, vy: pl.yVelocity,
    shields: pl.shields,
    score: myScore,
    ship: myShipIdx,
    lasers: myWireLasers(),
    name: myName,
  }));
}

function sendNpc(): void {
  if (!net) return;
  const shipsOut: P.NpcShip[] = [];
  for (let i = 1; i < ships.length; i++) {
    const s = ships[i];
    shipsOut.push({ id: s.npcId, flash: s.shieldTime > 0, sprite: s.npcSprite, x: s.x, y: s.y, angle: s.angle, vx: s.xVelocity, vy: s.yVelocity, shields: s.shields });
  }
  net.broadcastBestEffort(P.encodeNpc({ seq: npcSeq++ & 0xffff, ships: shipsOut, lasers: npcWireLasers() }));
}

/** Per-render-frame networking: leadership, easing, timeouts, respawn, sends. */
function netFrame(dt: number): void {
  if (!net) return;
  const now = performance.now();

  // Leadership: a fresh joiner waits HOST_GRACE_MS so an existing host's NPC
  // broadcast can arrive and be adopted instead of spawning a parallel world.
  const hosting = isHost() && now - joinedAt > P.HOST_GRACE_MS;
  if (hosting && !wasHost) becomeHost(now);
  if (!hosting && wasHost) resignHost();
  wasHost = hosting;
  if (hosting && ships.length - 1 < NPC_TARGET_COUNT && now >= nextPirateSpawnAt) {
    spawnPirate();
    nextPirateSpawnAt = now + 10000;
  }

  for (const [slot, r] of remotes) {
    if (now - r.lastRecv > P.REMOTE_TIMEOUT_MS) {
      remotes.delete(slot);
      continue;
    }
    easeRemote(r, dt, now);
  }
  for (const n of remoteNpcs.values()) easeRemote(n, dt, now);
  if (remoteNpcs.size > 0 && now - lastNpcRecv > P.REMOTE_TIMEOUT_MS) remoteNpcs.clear();

  if (!player().alive && now >= deadUntil) respawnMp();

  sendAcc += dt;
  if (sendAcc >= 1 / P.SEND_HZ) {
    sendAcc = 0;
    sendState();
    if (hosting) sendNpc();
  }
}

function onState(from: number, s: P.StateMsg): void {
  let r = remotes.get(from);
  if (!r) {
    r = {
      ship: new Ship(P.SPRITE_BASE + P.SHIP_SPRITES[s.ship % P.SHIP_SPRITES.length], s.x, s.y),
      name: s.name, shipIdx: s.ship, alive: s.alive, docked: s.docked, score: s.score,
      x: s.x, y: s.y, angle: s.angle, angleSpeed: s.angleSpeed, vx: s.vx, vy: s.vy,
      px: s.x, py: s.y, pa: s.angle,
      shields: s.shields, lasers: s.lasers, lastRecv: performance.now(), seq: s.seq,
    };
    remotes.set(from, r);
    return;
  }
  if (!P.seqNewer(s.seq, r.seq)) return;
  r.x = s.x; r.y = s.y; r.angle = s.angle; r.angleSpeed = s.angleSpeed;
  r.vx = s.vx; r.vy = s.vy;
  r.alive = s.alive; r.docked = s.docked; r.score = s.score;
  r.shields = s.shields; r.name = s.name; r.lasers = s.lasers;
  r.lastRecv = performance.now(); r.seq = s.seq;
  if (s.flash && r.ship.shieldTime <= 0) r.ship.shieldTime = 20;
  if (s.ship !== r.shipIdx) {
    r.shipIdx = s.ship;
    const img = new Image();
    img.src = P.SPRITE_BASE + P.SHIP_SPRITES[s.ship % P.SHIP_SPRITES.length];
    r.ship.sprite = img;
  }
}

function onDamage(from: number, d: P.DamageMsg): void {
  if (d.laserId !== P.NO_ID) {
    consumeLaser(d.shooterSlot, d.laserId); // never draw or re-apply that laser
    if (d.shooterSlot === selfSlot) {
      for (let i = laser.laserX.length - 1; i >= 0; i--) {
        if (laser.laserId[i] === d.laserId) {
          popList2Position(laser.allProperties, i);
          break;
        }
      }
    }
  }
  const r = remotes.get(from);
  if (r) {
    r.shields = d.shieldsAfter;
    r.ship.shieldTime = 20;
    if (d.died) r.alive = false;
  }
  if (d.died) {
    boom.createBoom(d.x, d.y);
    if (d.shooterSlot === selfSlot && !d.npcLaser) {
      myScore++;
      showToast(`you shot down ${r?.name ?? "a pilot"} — +1`);
    }
  }
}

function onNpcState(from: number, m: P.NpcMsg): void {
  if (from !== hostSlot() || isHost()) return; // only the current host is authoritative
  if (npcStateHasSeq && from === npcLasersFrom && !P.seqNewer(m.seq, npcStateSeq)) return;
  npcStateSeq = m.seq;
  npcStateHasSeq = true;
  const now = performance.now();
  const seen = new Set<number>();
  for (const s of m.ships) {
    seen.add(s.id);
    let n = remoteNpcs.get(s.id);
    if (!n) {
      n = {
        ship: new Ship(pirateSprite(s.sprite), s.x, s.y), sprite: s.sprite,
        x: s.x, y: s.y, angle: s.angle, angleSpeed: 0, vx: s.vx, vy: s.vy,
        px: s.x, py: s.y, pa: s.angle,
        shields: s.shields, lastRecv: now,
      };
      remoteNpcs.set(s.id, n);
    } else {
      n.x = s.x; n.y = s.y; n.angle = s.angle; n.vx = s.vx; n.vy = s.vy;
      n.shields = s.shields; n.lastRecv = now;
      if (n.sprite !== s.sprite) {
        n.sprite = s.sprite;
        const img = new Image();
        img.src = pirateSprite(s.sprite);
        n.ship.sprite = img;
      }
    }
    if (s.flash && n.ship.shieldTime <= 0) n.ship.shieldTime = 20;
  }
  for (const [id, n] of remoteNpcs) {
    if (!seen.has(id)) {
      boom.createBoom(n.px, n.py); // it was in the last snapshot: it blew up
      remoteNpcs.delete(id);
    }
  }
  npcLasers = m.lasers;
  npcLasersFrom = from;
  lastNpcRecv = now;
}

function onNpcDamage(from: number, m: P.NpcDamageMsg): void {
  if (!isHost() || !net) return;
  for (let i = 1; i < ships.length; i++) {
    if (ships[i].npcId === m.npcId) {
      ships[i].shields -= m.damage;
      ships[i].shieldTime = 20;
      if (ships[i].shields <= 0) {
        boom.createBoom(ships[i].x, ships[i].y);
        ships.splice(i, 1);
        // the reporter landed the killing blow: pay the bounty
        net.sendReliable(from, P.encodeNpcKill(m.npcId)).catch(() => {});
      }
      return;
    }
  }
}

function onNpcKill(): void {
  myScore++;
  showToast("pirate bounty +1");
}

function onNet(ev: P2PEvent): void {
  switch (ev.type) {
    case "message": {
      const msg = P.decode(ev.data);
      if (!msg) return;
      if (msg.kind === "state") onState(ev.from, msg);
      else if (msg.kind === "damage") onDamage(ev.from, msg);
      else if (msg.kind === "npc") onNpcState(ev.from, msg);
      else if (msg.kind === "npc-damage") onNpcDamage(ev.from, msg);
      else if (msg.kind === "npc-kill") onNpcKill();
      break;
    }
    case "player-left":
      remotes.delete(ev.playerId);
      showToast(`pilot in slot ${ev.playerId} left`);
      break;
    case "player-joined":
      showToast(`a pilot joined slot ${ev.playerId}`);
      break;
    case "signaling-closed":
      showToast("signaling closed: " + ev.code);
      if (ev.code === "replaced" || ev.code === "session-superseded" || ev.code === "room-expired") leaveMp();
      break;
    default:
      break;
  }
}

async function resolveServer(configured: string): Promise<string> {
  if (configured) return configured;
  try {
    const cfg = await (await fetch("./config.json")).json();
    if (cfg.wsUrl) return cfg.wsUrl;
  } catch {
    /* not served by the Go server; fall through */
  }
  return "https://pqrstuvw.xyz/lobbylink";
}

async function connectMp(): Promise<void> {
  const code = ($("code") as HTMLInputElement).value.trim() || "TARSUS";
  myName = (($("name") as HTMLInputElement).value.trim() || "pilot").slice(0, 24);
  const server = await resolveServer(($("server") as HTMLInputElement).value.trim());
  location.hash = code;
  showToast(`connecting to ${server} …`);
  let g: P2PGame;
  try {
    g = await P2PGame.connect({
      server, code,
      create: { maxPlayers: 16, waitUntilFull: false },
      storageKey: "tarsus-" + code,
      storage: "session",
    });
  } catch (err) {
    showToast("connect failed: " + String(err));
    return;
  }
  net = g;
  selfSlot = g.selfId;
  myShipIdx = selfSlot % P.SHIP_SPRITES.length;
  const img = new Image();
  img.src = P.SPRITE_BASE + P.SHIP_SPRITES[myShipIdx];
  player().sprite = img;
  g.onEvent(onNet);
  joinedAt = performance.now();
  wasHost = false;
  deadUntil = 0;
  sendAcc = 0;
  myScore = 0;
  remotes.clear();
  remoteNpcs.clear();
  consumedLasers.clear();
  npcLasers = [];
  npcLasersFrom = -1;
  npcStateHasSeq = false;
  resignHost(); // the NPCs now belong to whoever hosts; drop the local ones
  player().alive = true;
  mpMenu.style.display = "none";
  mpBtn.textContent = "🌐 Leave room";
  canvas.focus();
  showToast(`connected — room ${g.code}, you are slot ${selfSlot}`);
}

function leaveMp(): void {
  net?.close();
  net = null;
  remotes.clear();
  remoteNpcs.clear();
  npcLasers = [];
  npcLasersFrom = -1;
  wasHost = false;
  player().alive = true;
  hud.textContent = "";
  mpBtn.textContent = "🌐 Multiplayer";
  if (ships.length === 1) {
    // back to single player: give the pilot something to dogfight
    spawnPirate();
    spawnPirate();
  }
  showToast("left the room — flying solo");
  canvas.focus();
}

function showToast(text: string, ms = 2200): void {
  toastEl.textContent = text;
  toastEl.style.display = "block";
  toastUntil = performance.now() + ms;
}

function updateHud(): void {
  const me = player();
  const status = `score ${myScore} · shields ${Math.max(0, Math.round(me.shields))} · energy ${Math.round(me.energy)}`;
  if (!net) {
    hud.textContent = status;
    return;
  }
  let pilots = 1;
  for (const p of net.players) {
    if (p.occupied && p.connected && p.id !== selfSlot) pilots++;
  }
  hud.textContent =
    `room ${net.code} · ${pilots} pilot${pilots === 1 ? "" : "s"}${wasHost ? " · ★host" : ""}\n` +
    `${myName} — ${status}`;
}

function initUi(): void {
  const hashCode = decodeURIComponent(location.hash.replace(/^#/, ""));
  if (hashCode) ($("code") as HTMLInputElement).value = hashCode;
  mpBtn.addEventListener("click", () => {
    if (net) {
      leaveMp();
      return;
    }
    mpMenu.style.display = "flex";
    ($("name") as HTMLInputElement).focus();
  });
  $("connect").addEventListener("click", () => void connectMp());
  $("cancel").addEventListener("click", () => {
    mpMenu.style.display = "none";
    canvas.focus();
  });
}

//////////// remote rendering ///////////////////
const remoteLaserSprite = new Image();
remoteLaserSprite.src = P.SPRITE_BASE + "BallWhite.png";

function drawRemoteShip(ship: Ship, x: number, y: number, angle: number): void {
  ship.x = x;
  ship.y = y;
  ship.angle = angle;
  ship.draw(camera);
}

function drawLabel(wx: number, wy: number, text: string, offset = 34): void {
  ctx.save();
  // the world context is y-flipped; unflip so the text reads upright
  ctx.translate(wx - camera.cameraX[0] + canvas.width / 2, wy - camera.cameraY[0] + canvas.height / 2 + offset);
  ctx.scale(1, -1);
  ctx.font = "12px system-ui, sans-serif";
  ctx.fillStyle = "rgba(220,230,255,0.8)";
  ctx.textAlign = "center";
  ctx.fillText(text, 0, 0);
  ctx.restore();
}

function drawBases(): void {
  for (const b of bases) {
    const dx = b.x - camera.cameraX[0];
    const dy = b.y - camera.cameraY[0];
    if (Math.abs(dx) > canvas.width / 2 + 300 || Math.abs(dy) > canvas.height / 2 + 300) continue;
    const img = baseSprites[b.sprite];
    ctx.save();
    ctx.translate(dx + canvas.width / 2, dy + canvas.height / 2);
    ctx.drawImage(img, 0, 0, img.width, img.height, -img.width / 2, -img.height / 2, img.width, img.height);
    ctx.restore();
    drawLabel(b.x, b.y, b.name, -Math.max(44, img.height / 2 + 16));
  }
}

/**
 * Point at an off-screen thing (a fellow pilot, the nearest base) with a small
 * arrow pinned to the screen edge — space is vast. No-op when it is on screen.
 */
function drawEdgeMarker(wx: number, wy: number, color: string, label: string): void {
  const dx = wx - camera.cameraX[0];
  const dy = wy - camera.cameraY[0];
  const sx = dx + canvas.width / 2;
  const sy = dy + canvas.height / 2;
  const m = 18;
  if (sx >= m && sx <= canvas.width - m && sy >= m && sy <= canvas.height - m) return;
  const cx = Math.min(Math.max(sx, m), canvas.width - m);
  const cy = Math.min(Math.max(sy, m), canvas.height - m);
  const ang = Math.atan2(sy - cy, sx - cx);
  ctx.save();
  ctx.translate(cx, cy);
  ctx.rotate(ang);
  ctx.beginPath();
  ctx.moveTo(10, 0);
  ctx.lineTo(-6, 5);
  ctx.lineTo(-6, -5);
  ctx.closePath();
  ctx.fillStyle = color;
  ctx.fill();
  ctx.restore();
  const d = Math.hypot(dx, dy);
  ctx.save();
  ctx.translate(cx - Math.cos(ang) * 16, cy - Math.sin(ang) * 16 - 12);
  ctx.scale(1, -1);
  ctx.font = "11px system-ui, sans-serif";
  ctx.fillStyle = color;
  ctx.textAlign = Math.abs(cx - canvas.width / 2) > canvas.width / 2 - 60 ? (cx < canvas.width / 2 ? "left" : "right") : "center";
  ctx.fillText(`${label} ${(d / 1000).toFixed(1)}k`, 0, 0);
  ctx.restore();
}

function drawCenterText(text: string): void {
  ctx.save();
  ctx.translate(canvas.width / 2, 64); // flipped coords: 64 px up from the bottom
  ctx.scale(1, -1);
  ctx.font = "bold 14px system-ui, sans-serif";
  ctx.fillStyle = "#7fe9ff";
  ctx.textAlign = "center";
  ctx.fillText(text, 0, 0);
  ctx.restore();
}

function drawDockPrompt(): void {
  const p = player();
  if (!p.alive) return;
  if (p.docked) {
    drawCenterText(`docked at ${dockedBase?.name ?? "base"} — press L to launch`);
    return;
  }
  const b = dockableBase();
  if (b) drawCenterText(`press L to dock at ${b.name}`);
}

function drawMarkers(): void {
  for (const r of remotes.values()) {
    if (r.alive) drawEdgeMarker(r.px, r.py, "#7fe9ff", r.name);
  }
  let nearest: P.Base | null = null;
  let nearestD = Infinity;
  for (const b of bases) {
    const d = dist(b.x, b.y, player().x, player().y);
    if (d < nearestD) {
      nearestD = d;
      nearest = b;
    }
  }
  if (nearest) drawEdgeMarker(nearest.x, nearest.y, "#9fb0da", nearest.name);
}

function drawWireLaser(img: HTMLImageElement, wx: number, wy: number): void {
  const size = 0.25;
  ctx.save();
  ctx.translate(wx - camera.cameraX[0] + canvas.width / 2, wy - camera.cameraY[0] + canvas.height / 2);
  ctx.drawImage(img, 0, 0, img.width, img.height, -img.width / 2 * size, -img.height / 2 * size, img.width * size, img.height * size);
  ctx.restore();
}

function drawRemoteLasers(): void {
  const now = performance.now();
  for (const [slot, r] of remotes) {
    const age = Math.min((now - r.lastRecv) / 1000, 0.4);
    for (const l of r.lasers) {
      if (consumedLasers.has(laserKey(slot, l.id))) continue;
      drawWireLaser(remoteLaserSprite, l.x + l.vx * 60 * age, l.y + l.vy * 60 * age);
    }
  }
  if (npcLasersFrom >= 0 && npcLasersFrom !== selfSlot) {
    const age = Math.min((now - lastNpcRecv) / 1000, 0.4);
    for (const l of npcLasers) {
      if (consumedLasers.has(laserKey(npcLasersFrom, l.id))) continue;
      drawWireLaser(laser.sprite[0], l.x + l.vx * 60 * age, l.y + l.vy * 60 * age);
    }
  }
}

//////////// Gameloop ///////////////////////////
function pirateTarget(pirate: Ship): { x: number; y: number } | null {
  if (!netActive()) {
    if (ships[0].docked) return null;
    return { x: ships[0].x, y: ships[0].y };
  }
  let best: { x: number; y: number } | null = null;
  let bestD = Infinity;
  if (player().alive && !player().docked) {
    best = { x: player().x, y: player().y };
    bestD = dist(player().x, player().y, pirate.x, pirate.y);
  }
  for (const r of remotes.values()) {
    if (!r.alive || r.docked) continue;
    const d = dist(r.px, r.py, pirate.x, pirate.y);
    if (d < bestD) {
      bestD = d;
      best = { x: r.px, y: r.py };
    }
  }
  return best;
}

function step() {
  if (player().alive && !player().docked) {
    if (player().angleSpeed >= 5){
      player().angleSpeed = 5
    }
    if (player().angleSpeed <= -5){
      player().angleSpeed = -5
    }
    if (player().leftPressed){
      player().angleSpeed += 0.125
    }
    if (player().rightPressed){
      player().angleSpeed -= 0.125
    }
    if (!player().rightPressed && !player().leftPressed) {
      player().angleSpeed /= 1.05
    }
    if (!player().tabPressed) {
      if (player().upPressed) {
        player().xVelocity +=player().acceleration*K_SCALE*Math.cos(player().angle*Math.PI/180);
        player().yVelocity +=player().acceleration*K_SCALE*Math.sin(player().angle*Math.PI/180);
      }
      if (player().downPressed) {
        player().xVelocity -=player().acceleration*K_SCALE*Math.cos(player().angle*Math.PI/180);
        player().yVelocity -=player().acceleration*K_SCALE*Math.sin(player().angle*Math.PI/180);
      }
    }else{
      if (player().upPressed) {
        player().xVelocity +=player().afterburnerAcceleration*K_SCALE*Math.cos(player().angle*Math.PI/180);
        player().yVelocity +=player().afterburnerAcceleration*K_SCALE*Math.sin(player().angle*Math.PI/180);
      }
      if (player().downPressed) {
        player().xVelocity -=player().afterburnerAcceleration*K_SCALE*Math.cos(player().angle*Math.PI/180);
        player().yVelocity -=player().afterburnerAcceleration*K_SCALE*Math.sin(player().angle*Math.PI/180);
      }
    }
  }
  const shipBefore = player();
  const shieldsBefore = shipBefore.shields;
  for(let i = ships.length - 1; i >=0;i--) {
    if (ships[i] !== player())
    {
      const t = pirateTarget(ships[i]);
      if (t) ships[i].ai(t.x, t.y);
    }
    let isAlive = true;
    if (ships[i].alive && !ships[i].docked) {
      isAlive = ships[i].update()
    }
    if(!isAlive){
      if (netActive() && ships[i] === player()) {
        // a pirate laser from the local pool got me (host only)
        dieMp(selfSlot, P.NO_ID, true, true);
      } else {
        if (ships[i] !== player() && ships[i].lastHitBy === player()) {
          myScore++;
          showToast("pirate bounty +1");
        }
        ships.splice(i, 1)
      }
    }
  }
  if (player().docked) {
    // repairing and rearming in the hangar
    player().shields = Math.min(player().shieldsMax, player().shields + 2);
    player().energy = Math.min(player().maxEnergy, player().energy + 10);
  }
  if (netActive() && player() === shipBefore && shipBefore.alive && shipBefore.shields <= shieldsBefore - P.LASER_DAMAGE + 1) {
    // a pirate laser from the local pool chipped my shields; tell the room
    broadcastDamage({ died: false, npcLaser: true, shooterSlot: selfSlot, laserId: P.NO_ID, shieldsAfter: shipBefore.shields, x: shipBefore.x, y: shipBefore.y });
  }

  if(player().alive && !player().docked && player().spacePressed) {
    shoot(player());
  }
  if (player().alive && !player().upPressed && !player().downPressed) {
    player().xVelocity /= 1.0395
    player().yVelocity /= 1.0395
  }
  laser.update();
  boom.update();
  netStep();
  camera.update(player().x,player().y)
}

function render() {
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  ctx.save()
  ctx.translate(0, canvas.height);
  ctx.scale(1,-1);
  stars.draw(camera);
  /// asteroids.draw(camera);
  drawBases();
  laser.draw(camera);
  drawRemoteLasers();
  for (const n of remoteNpcs.values()) {
    drawRemoteShip(n.ship, n.px, n.py, n.pa);
  }
  for (const r of remotes.values()) {
    if (!r.alive) continue;
    drawRemoteShip(r.ship, r.px, r.py, r.pa);
    drawLabel(r.px, r.py, r.docked ? `${r.name} · ${r.score} ⚓` : `${r.name} · ${r.score}`);
  }
  for(let i = 0; i<ships.length;i++) {
    if (ships[i].alive) ships[i].draw(camera)
  }
  boom.draw(camera);
  drawMarkers();
  drawDockPrompt();
  ctx.restore();
}

// The simulation always steps at 60 Hz regardless of display refresh rate, so
// a 120 Hz laptop and a 60 Hz monitor fly the same physics.
let lastFrameMs = performance.now();
let stepAcc = 0;
function frame() {
  const now = performance.now();
  let dt = (now - lastFrameMs) / 1000;
  lastFrameMs = now;
  if (dt > 0.25) dt = 0.25; // tab was hidden; don't fast-forward
  stepAcc += dt;
  let steps = 0;
  while (stepAcc >= P.TICK && steps < 4) {
    step();
    stepAcc -= P.TICK;
    steps++;
  }
  if (steps === 4) stepAcc = 0;
  netFrame(dt);
  updateHud();
  if (now > toastUntil) toastEl.style.display = "none";
  render();
  requestAnimationFrame(frame);
}

// live handles for debugging and automated tests
(window as any).tarsus = { ships, remotes, remoteNpcs, bases, laser, player, netActive, isHost, score: () => myScore };

initUi();
requestAnimationFrame(frame);
