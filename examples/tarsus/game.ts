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
  public draw(camera:Camera) {
    for(var i = 0; this.boomX.length > i;i++) {
      ctx.save();
      ctx.translate(this.boomX[i]-camera.cameraX[0]+canvas.width/2,this.boomY[i]-camera.cameraY[0]+canvas.height/2);
      ctx.drawImage(this.boomImages[this.boomFrame[i]], 0, 0, this.boomImages[this.boomFrame[i]].width, this.boomImages[this.boomFrame[i]].height,
                    -this.boomImages[this.boomFrame[i]].width/2*this.size, -this.boomImages[this.boomFrame[i]].height/2*this.size, this.boomImages[this.boomFrame[i]].width*this.size, this.boomImages[this.boomFrame[i]].height*this.size);
      ctx.restore();
      this.boomFrame[i]++
      if (this.boomFrame[i]>=this.boomImages.length) {
        popList2Position([this.boomX,this.boomY,this.boomFrame],i);
        i--;
      } 
    }
  }
}
/////////// lazzzzers!!! ///////////
class Lasers {
  laserX:     number[] = []
  laserY:     number[] = []
  laserStartX:number[] = []
  laserStartY:number[] = []
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
    this.laserEntityStartId.push(entity);

  }
  constructor(imageUrl:string[]) {
    this.size = 0.25;
    this.allProperties = [this.laserX, this.laserY, this.laserVelocityX, this.laserVelocityY, this.laserStartX, this.laserStartY, this.laserEntityStartId];
    for (var i = 0; i < imageUrl.length;i++) {
      let image = new Image();
      image.src = imageUrl[i];
      this.sprite.push(image);
    }
  }
      
  public draw(camera:Camera) {
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
  target: number = 0
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
    if(this.energy>=this.shieldRegenerationcost&&this.shields<this.shieldsMax) {
      this.energy -= this.shieldRegenerationcost
      this.shields += this.shieldRegeneration
    }
    if(this.shields>this.shields) {
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
  public ai() {
    let targetAngle = (Math.atan2(ships[this.target].y-this.y,ships[this.target].x-this.x)*180/Math.PI + 360) % 360;
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
    shoot(this)
    this.xVelocity = this.maxSetSpeed * Math.cos(toRadians(this.angle)) *.1;
    this.yVelocity = this.maxSetSpeed * Math.sin(toRadians(this.angle)) *.1;
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
//////////// Gameloop ///////////////////////////
function gameloop() {
  
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
  for(let i = ships.length - 1; i >=0;i--) {
    if (ships[i] !== player())
    {
      ships[i].ai();
    }
    let isAlive = ships[i].update()
    if(!isAlive){
      ships.splice(i, 1)
    }
  }

  if(player().spacePressed) {
    shoot(player());
  }
  if (!player().upPressed && !player().downPressed) {
    player().xVelocity /= 1.0395
    player().yVelocity /= 1.0395
  }
  camera.update(player().x,player().y)
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  ctx.save()
  ctx.translate(0, canvas.height);
  ctx.scale(1,-1);
  stars.draw(camera);
  /// asteroids.draw(camera);
  laser.draw(camera);
  for(let i = 0; i<ships.length;i++) {
    ships[i].draw(camera)
  }
  boom.draw(camera);
  requestAnimationFrame(gameloop);
  ctx.restore();
}


gameloop();
